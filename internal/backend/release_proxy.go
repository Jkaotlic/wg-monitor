package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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

func releaseAssetProxyHandler(d Deps) http.HandlerFunc {
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
		if d.ReleaseCacheDir != "" {
			serveReleaseBinaryCached(w, r, d.ReleaseCacheDir, version, asset, u)
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

// serveReleaseBinaryCached -- бинарь выпуска через дисковый кэш (SEC-01).
//
// Раздача без авторизации, и медленный клиент держал слот до 90 с: два таких
// стопорили раскатку всего парка. Теперь слот держит только поход на GitHub
// (с таймаутом, не зависящим от клиента), а клиенту файл отдаётся с диска,
// уже без слота и без копии в памяти. В кэш попадает только бинарь, чья
// sha256 совпала с подписанным checksums.txt выпуска: выпуск неизменен, и
// проверенный файл годится всем следующим.
func serveReleaseBinaryCached(w http.ResponseWriter, r *http.Request, cacheDir, version, asset, u string) {
	path := filepath.Join(cacheDir, version, asset)
	if serveReleaseCacheFile(w, r, path) {
		return
	}
	select {
	case releaseProxySlots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", releaseProxyRetryAfter())
		writeJSONError(w, http.StatusServiceUnavailable, errCodeInternal, "release proxy busy; retry shortly")
		return
	}
	status, msg := fillReleaseCache(r.Context(), path, version, asset, u)
	<-releaseProxySlots
	if status != http.StatusOK {
		writeJSONError(w, status, errCodeInternal, msg)
		return
	}
	if !serveReleaseCacheFile(w, r, path) {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "release cache read failed")
	}
}

// fillReleaseCache забирает бинарь, сверяет с подписанными суммами и
// атомарно кладёт в кэш. Поход на GitHub не привязан к соединению клиента:
// отвалившийся клиент не должен выбрасывать уже почти скачанный файл.
func fillReleaseCache(ctx context.Context, path, version, asset, u string) (int, string) {
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	sums, err := verifiedChecksumsFetcher(fetchCtx, releaseDownloadBase, version)
	if err != nil {
		return http.StatusBadGateway, "release checksums: " + err.Error()
	}
	want := strings.ToLower(strings.TrimSpace(sums[asset]))
	if want == "" {
		return http.StatusBadGateway, "release checksums: no entry for " + asset
	}
	got, status, msg := fetchReleaseProxyAsset(fetchCtx, u, maxSelfUpdateProxyBytes, 90*time.Second)
	if status != http.StatusOK {
		return status, msg
	}
	sum := sha256.Sum256(got.body)
	if hex.EncodeToString(sum[:]) != want {
		return http.StatusBadGateway, "release fetch: sha256 mismatch for " + asset
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return http.StatusInternalServerError, "release cache: " + err.Error()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+asset+".tmp-*")
	if err != nil {
		return http.StatusInternalServerError, "release cache: " + err.Error()
	}
	_, werr := tmp.Write(got.body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return http.StatusInternalServerError, "release cache write failed"
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return http.StatusInternalServerError, "release cache: " + err.Error()
	}
	pruneReleaseCache(filepath.Dir(filepath.Dir(path)), releaseCacheKeepVersions)
	return http.StatusOK, ""
}

// releaseCacheKeepVersions -- сколько выпусков держать в кэше: бэкенд живёт
// на Raspberry Pi, и копить все бинари всех версий незачем.
const releaseCacheKeepVersions = 3

// pruneReleaseCache оставляет keep самых свежих (по времени изменения)
// каталогов версий. Ошибки не мешают раздаче: кэш -- удобство.
func pruneReleaseCache(root string, keep int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type dirAt struct {
		name string
		at   time.Time
	}
	var dirs []dirAt
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirAt{e.Name(), info.ModTime()})
	}
	if len(dirs) <= keep {
		return
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].at.After(dirs[j].at) })
	for _, d := range dirs[keep:] {
		_ = os.RemoveAll(filepath.Join(root, d.name))
	}
}

// serveReleaseCacheFile отдаёт файл кэша; false -- файла нет.
func serveReleaseCacheFile(w http.ResponseWriter, r *http.Request, path string) bool {
	f, err := os.Open(path) // #nosec G304 -- путь из проверенных тега и имени файла
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", st.ModTime(), f)
	return true
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
