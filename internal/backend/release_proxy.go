package backend

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

var releaseDownloadBase = "https://github.com/Jkaotlic/wg-monitor/releases/download"

// Раздача бинарей идёт через память: чтобы отличить «файл кончился раньше
// времени» от «файл слишком большой», ответ проверяется целиком до первого
// записанного байта (см. ниже). Плата за это -- память на каждого
// скачивающего, а бэкенд живёт на Raspberry Pi.
//
// Вчера это выстрелило: перезапуски раз за разом ставили в очередь
// self_update сразу четырём роутерам, и они пошли за бинарями одновременно.
// Двух одновременных раздач хватает для парка любой величины -- агент,
// получивший отказ, вернётся сам. Слоты держат только бинари: суммы и
// подпись идут из памяти (releaseSmallAssets ниже). Это же число ограничивает,
// скольким роутерам разом выдаётся self_update (deploy_dispatch.go).
const maxConcurrentReleaseProxy = 2

var releaseProxySlots = make(chan struct{}, maxConcurrentReleaseProxy)

func releaseAssetProxyHandler(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		version := strings.TrimSpace(r.PathValue("version"))
		asset := strings.TrimSpace(r.PathValue("asset"))
		version, tagErr := releaseorigin.ValidateReleaseTag(version)
		if tagErr != nil || !isAllowedReleaseAsset(asset) {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "invalid release asset")
			return
		}
		u := strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(version) + "/" + url.PathEscape(asset)
		if limit, small := releaseSmallAssetLimit(asset); small {
			serveReleaseSmallAsset(w, r, u, limit)
			return
		}
		select {
		case releaseProxySlots <- struct{}{}:
			defer func() { <-releaseProxySlots }()
		default:
			// Не ждём в очереди: держать соединение открытым значит держать и
			// память, ради которой всё это и затевалось.
			w.Header().Set("Retry-After", releaseProxyRetryAfter())
			writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "release proxy busy; retry shortly")
			return
		}
		// Shares releaseFetchTransport (release_verify.go) — same CDN, same
		// flaky-home-uplink TLS-handshake fragility — but keeps its own
		// longer overall timeout since this proxies a full binary/archive,
		// not a small checksums file. Deliberately no retry here: unlike
		// fetchReleaseVerifyAsset's plain fetch, this handler already
		// distinguishes oversized-Content-Length, oversized-chunked, and
		// truncated-body failures with their own status codes/messages
		// (see wizard_handler_test.go's ReleaseAssetProxy* tests), and
		// retrying would risk conflating a retryable transport hiccup with
		// those non-retryable validation failures.
		got, status, msg := fetchReleaseProxyAsset(r.Context(), u, maxSelfUpdateProxyBytes, 90*time.Second)
		if status != http.StatusOK {
			writeJSONError(w, status, errCodeInternal, msg)
			return
		}
		writeReleaseAsset(w, got)
	}
}

// releaseProxyRetryAfter -- сколько секунд просить подождать при отказе
// «занято». С разбросом 10..40: одинаковое «30» у всех возвращало толпу
// обратно разом, и она снова упиралась в те же два слота.
func releaseProxyRetryAfter() string {
	return strconv.Itoa(10 + rand.IntN(31)) // #nosec G404 -- разброс повторов, не секрет
}

// releaseSmallAsset -- тело маленького файла выпуска вместе с его типом.
type releaseSmallAsset struct {
	body        []byte
	contentType string
	fetchedAt   time.Time
}

// fetchReleaseProxyAsset забирает файл выпуска целиком в память и проверяет
// размер до первого записанного клиенту байта. Возвращает HTTP-статус для
// клиента и текст ошибки; 200 -- всё в порядке.
func fetchReleaseProxyAsset(ctx context.Context, u string, limit int64, timeout time.Duration) (releaseSmallAsset, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return releaseSmallAsset{}, http.StatusInternalServerError, err.Error()
	}
	resp, err := (&http.Client{Timeout: timeout, Transport: releaseFetchTransport}).Do(req)
	if err != nil {
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: HTTP " + resp.Status
	}
	if resp.ContentLength > limit {
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: response too large"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: " + err.Error()
	}
	if int64(len(body)) > limit {
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: response too large"
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return releaseSmallAsset{body: body, contentType: ct, fetchedAt: time.Now()}, http.StatusOK, ""
}

func writeReleaseAsset(w http.ResponseWriter, a releaseSmallAsset) {
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(a.body)))
	_, _ = w.Write(a.body)
}

// releaseSmallAssetLimit -- лимит размера для маленьких файлов выпуска, тех
// самых, что проверяет и сам бэкенд (release_verify.go). Бинари сюда не
// попадают: они идут через слоты.
func releaseSmallAssetLimit(asset string) (int64, bool) {
	switch asset {
	case "checksums.txt":
		return maxVerifiedChecksumsBytes, true
	case "checksums.txt.sig":
		return maxVerifiedChecksumsSigBytes, true
	default:
		return 0, false
	}
}

// Суммы и подпись -- килобайты, одинаковые для всех роутеров одной версии.
// Прод, 18.09: все 19 провалов раскатки были 503 именно на них, пока два
// слота держали перекачку бинарей. Теперь они отдаются из памяти без слота:
// первый пришедший идёт на GitHub, остальные ждут его ответа, следующие
// получают готовое.
//
// Ключ -- полный адрес выпуска (база + версия + файл): выпуск неизменен, а
// час жизни записи страхует от перевыпуска с новой подписью.
const (
	releaseSmallAssetCacheMax = 16
	releaseSmallAssetTTL      = time.Hour
	releaseSmallAssetTimeout  = 60 * time.Second
)

var releaseSmallAssets = newReleaseSmallAssetCache(releaseSmallAssetCacheMax)

type releaseSmallFetch struct {
	done   chan struct{}
	asset  releaseSmallAsset
	status int
	msg    string
}

type releaseSmallAssetCache struct {
	mu       sync.Mutex
	max      int
	order    []string // порядок постановки, старые в начале
	entries  map[string]releaseSmallAsset
	inflight map[string]*releaseSmallFetch
}

func newReleaseSmallAssetCache(maxEntries int) *releaseSmallAssetCache {
	return &releaseSmallAssetCache{
		max:      maxEntries,
		entries:  make(map[string]releaseSmallAsset),
		inflight: make(map[string]*releaseSmallFetch),
	}
}

func (c *releaseSmallAssetCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *releaseSmallAssetCache) get(key string) (releaseSmallAsset, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(key, time.Now())
}

func (c *releaseSmallAssetCache) getLocked(key string, now time.Time) (releaseSmallAsset, bool) {
	a, ok := c.entries[key]
	if !ok || (!a.fetchedAt.IsZero() && now.Sub(a.fetchedAt) > releaseSmallAssetTTL) {
		return releaseSmallAsset{}, false
	}
	return a, true
}

func (c *releaseSmallAssetCache) put(key string, a releaseSmallAsset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, a)
}

func (c *releaseSmallAssetCache) putLocked(key string, a releaseSmallAsset) {
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = a
	for len(c.order) > c.max {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// fetch отдаёт запись из памяти или забирает её один раз на всех, кто
// пришёл одновременно. Ошибка не запоминается: следующий вправе попробовать
// снова.
func (c *releaseSmallAssetCache) fetch(ctx context.Context, key string, load func(context.Context) (releaseSmallAsset, int, string)) (releaseSmallAsset, int, string) {
	c.mu.Lock()
	if a, ok := c.getLocked(key, time.Now()); ok {
		c.mu.Unlock()
		return a, http.StatusOK, ""
	}
	f, running := c.inflight[key]
	if !running {
		f = &releaseSmallFetch{done: make(chan struct{})}
		c.inflight[key] = f
	}
	c.mu.Unlock()

	if !running {
		// Отвалившийся первый клиент не должен ронять остальных: запрос на
		// GitHub живёт своим сроком, а не соединением того, кто его начал.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseSmallAssetTimeout)
		f.asset, f.status, f.msg = load(loadCtx)
		cancel()
		c.mu.Lock()
		if f.status == http.StatusOK {
			c.putLocked(key, f.asset)
		}
		delete(c.inflight, key)
		c.mu.Unlock()
		close(f.done)
		return f.asset, f.status, f.msg
	}
	select {
	case <-f.done:
		return f.asset, f.status, f.msg
	case <-ctx.Done():
		return releaseSmallAsset{}, http.StatusBadGateway, "release fetch: " + ctx.Err().Error()
	}
}

func serveReleaseSmallAsset(w http.ResponseWriter, r *http.Request, u string, limit int64) {
	got, status, msg := releaseSmallAssets.fetch(r.Context(), u, func(ctx context.Context) (releaseSmallAsset, int, string) {
		return fetchReleaseProxyAsset(ctx, u, limit, releaseSmallAssetTimeout)
	})
	if status != http.StatusOK {
		writeJSONError(w, status, errCodeInternal, msg)
		return
	}
	writeReleaseAsset(w, got)
}

const maxSelfUpdateProxyBytes = 64 << 20

func isAllowedReleaseAsset(asset string) bool {
	switch asset {
	case "checksums.txt",
		"checksums.txt.sig",
		"wg-monitor-agent-linux-arm64",
		"wg-monitor-agent-linux-mipsle",
		"wg-monitor-backend-linux-amd64",
		"wg-monitor-backend-linux-arm64":
		return true
	default:
		return false
	}
}
