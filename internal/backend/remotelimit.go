package backend

import (
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Лимит попыток на входах в систему -- том самом месте, где короткое
// случайное значение защищено только своей длиной.
//
// У лимитера отчётов (ratelimit.go) ключ -- токен агента, и на входе он
// бесполезен: тот, кто подбирает ссылку, токена не предъявляет вовсе.
// Поэтому здесь ключ -- адрес, с которого пришли.
const (
	// Три входа: /v1/dashboard/login, /v1/dashboard/web-link/redeem и
	// /v1/miniapp/session. Запас в 10 попыток -- чтобы человек, у которого
	// сорвалась связь на релее, мог перезайти несколько раз подряд; дальше
	// одна попытка в пять секунд.
	entranceRatePerSec = 0.2
	entranceBurst      = 10
	// Потолок числа адресов в памяти. Ключ приходит снаружи, значит карта
	// ведёрок -- это то, что перебором можно раздуть: без потолка защита от
	// подбора сама становится способом съесть память бэкенда.
	remoteRateLimiterMaxBuckets = 4096
)

// remoteRateLimiter -- token bucket на адрес обращения.
type remoteRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // токенов в секунду
	burst   float64 // ёмкость ведёрка
	now     func() time.Time
	// backstop -- потолок по RemoteAddr, не зависящий от заголовков: если
	// прокси пропускает X-Forwarded-For клиента как есть, перебором заголовка
	// ведёрко клиента не обойти. Десятикратный запас, считает каждый запрос.
	backstop *remoteRateLimiter
	// ignoreForwarded -- не читать X-Forwarded-For: ключ всегда RemoteAddr
	// (config trust_forwarded_for: false). Нулевое значение -- верить, как и
	// было до флага.
	ignoreForwarded bool
}

func newRemoteRateLimiter(perSec float64, burst int) *remoteRateLimiter {
	if perSec <= 0 || burst <= 0 {
		return nil
	}
	l := &remoteRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    perSec,
		burst:   float64(burst),
		now:     time.Now,
	}
	l.backstop = &remoteRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    perSec * 10,
		burst:   float64(burst) * 10,
		now:     func() time.Time { return l.now() },
	}
	return l
}

// Allow тратит один токен адреса. nil-лимитер пропускает всё: это
// «ограничение выключено», а не паника на каждом запросе.
func (l *remoteRateLimiter) Allow(remote string) (ok bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	key := remoteRateKey(remote)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= remoteRateLimiterMaxBuckets {
			l.evictLocked(now)
		}
		l.buckets[key] = &tokenBucket{tokens: l.burst - 1, lastFill: now}
		return true, 0
	}
	b.tokens = l.tokensAt(b, now)
	b.lastFill = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing/l.rate*float64(time.Second)) + time.Second
	return false, wait
}

func (l *remoteRateLimiter) tokensAt(b *tokenBucket, now time.Time) float64 {
	tokens := b.tokens
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		tokens += elapsed * l.rate
	}
	if tokens > l.burst {
		tokens = l.burst
	}
	return tokens
}

// evictLocked освобождает место под новый адрес.
//
// Жертва выбирается НЕ произвольно, и это защита, а не оптимизация. Раньше
// здесь удалялись случайные ключи, и этим открывался обход: залив карты
// чужими адресами (для владельца одного IPv6 /64 он бесплатен) выбрасывал
// ведёрко самого подбирающего, и счёт его попыток начинался заново.
//
// Правило: уходит тот, кому терять нечего. Сначала полные ведёрки -- лимит о
// таком адресе всё равно ничего не помнит. Если места всё ещё нет, значит
// идёт распределённый залив, и тогда уходят САМЫЕ ПОЛНЫЕ: наказанное ведёрко
// (пустое) остаётся последним и переживает залив.
//
// Освобождается сразу четверть карты, поэтому сортировка приходится не на
// каждую вставку, а раз на тысячу: под заливом это разница между «дорого» и
// «незаметно».
func (l *remoteRateLimiter) evictLocked(now time.Time) {
	for key, b := range l.buckets {
		if l.tokensAt(b, now) >= l.burst {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < remoteRateLimiterMaxBuckets {
		return
	}
	type victim struct {
		key    string
		tokens float64
	}
	victims := make([]victim, 0, len(l.buckets))
	for key, b := range l.buckets {
		victims = append(victims, victim{key: key, tokens: l.tokensAt(b, now)})
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].tokens > victims[j].tokens })
	target := remoteRateLimiterMaxBuckets * 3 / 4
	for _, v := range victims {
		if len(l.buckets) <= target {
			return
		}
		delete(l.buckets, v.key)
	}
}

func (l *remoteRateLimiter) size() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// remoteRateKey -- адрес без порта. Порт у подбирающего меняется на каждом
// запросе, и ключ по нему не ограничил бы ничего вовсе.
func remoteRateKey(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		return host
	}
	return remote
}

// trustedHop -- адрес прокси, которому можно верить в заголовках: loopback,
// частная сеть (RFC1918, ULA fc00::/7) и link-local. Мост docker и роутер
// KeenDNS приходят именно такими.
func trustedHop(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// clientRateKey -- ключ лимита: настоящий клиент, а не прокси перед нами.
// X-Forwarded-For читается ТОЛЬКО если RemoteAddr -- доверенный прокси (иначе
// заголовок подделывается одной строкой). В XFF идём справа налево и берём
// первый недоверенный адрес: клиент может дописать свои адреса слева, но
// справа ставит запись только наш прокси. X-Real-IP не читается вовсе: при
// проброске заголовков прокси он целиком в руках клиента. Нет годного
// заголовка -- RemoteAddr.
func clientRateKey(r *http.Request) string { return clientRateKeyOpt(r, true) }

// clientRateKeyOpt -- clientRateKey с выключателем: trustForwarded=false
// возвращает RemoteAddr, не заглядывая в заголовки.
func clientRateKeyOpt(r *http.Request, trustForwarded bool) string {
	remote := remoteRateKey(r.RemoteAddr)
	rip := net.ParseIP(remote)
	if !trustForwarded || rip == nil || !trustedHop(rip) {
		return remote
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break // мусор в цепочке: дальше слева верить нечему
		}
		if !trustedHop(ip) {
			return ip.String()
		}
	}
	return remote
}

// Разовые записи в журнал: первый запрос за прокси с X-Forwarded-For и первый
// без него. Живьём не известно, дописывает ли его KeenDNS; следующий взгляд в
// журнал должен ответить на это без новой раскопки.
var (
	loggedXFFPresent atomic.Bool
	loggedXFFAbsent  atomic.Bool
)

// resetForwardedObs -- для тестов: забыть, что уже писали.
func resetForwardedObs() {
	loggedXFFPresent.Store(false)
	loggedXFFAbsent.Store(false)
}

// redactForwardedChain прячет адреса цепочки до сети: IPv4 до /24, IPv6 до /64.
func redactForwardedChain(chain string) string {
	parts := strings.Split(chain, ",")
	for i, p := range parts {
		ip := net.ParseIP(strings.TrimSpace(p))
		switch {
		case ip == nil:
			parts[i] = "?"
		case ip.To4() != nil:
			parts[i] = (&net.IPNet{IP: ip.To4().Mask(net.CIDRMask(24, 32)), Mask: net.CIDRMask(24, 32)}).String()
		default:
			parts[i] = (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
		}
	}
	return strings.Join(parts, ", ")
}

// noteForwardedPresence считает запросы за прокси (частный RemoteAddr) с
// X-Forwarded-For и без и один раз на процесс пишет по примеру каждого.
func noteForwardedPresence(r *http.Request, logger *slog.Logger) {
	rip := net.ParseIP(remoteRateKey(r.RemoteAddr))
	if rip == nil || !trustedHop(rip) {
		return
	}
	chain := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if strings.TrimSpace(chain) == "" {
		metricRemoteXFFAbsent.Add(1)
		if logger != nil && loggedXFFAbsent.CompareAndSwap(false, true) {
			logger.Info("вход: запрос за прокси без X-Forwarded-For",
				"proxy", redactForwardedChain(remoteRateKey(r.RemoteAddr)))
		}
		return
	}
	metricRemoteXFFPresent.Add(1)
	if logger != nil && loggedXFFPresent.CompareAndSwap(false, true) {
		logger.Info("вход: запрос за прокси с X-Forwarded-For",
			"proxy", redactForwardedChain(remoteRateKey(r.RemoteAddr)),
			"chain", redactForwardedChain(chain))
	}
}

// statusRecorder запоминает код ответа, чтобы вернуть токен за удачный вход.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// refund возвращает один токен ключу (не выше ёмкости).
func (l *remoteRateLimiter) refund(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok {
		b.tokens = l.tokensAt(b, l.now()) + 1
		b.lastFill = l.now()
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
}

// pathSecret -- длинная шестнадцатеричная строка в пути: так выглядит любой
// секрет в адресе (одноразовый билет, токен).
var pathSecret = regexp.MustCompile(`[0-9a-fA-F]{32,}`)

// redactPathSecrets прячет секреты в пути перед записью в журнал: отказ по
// лимиту не тратит секрет, и путь целиком в журнале был бы рабочей ссылкой.
func redactPathSecrets(path string) string {
	return pathSecret.ReplaceAllString(path, "***")
}

// remoteRateLimitMiddleware ставит лимит попыток перед входом. Отказ говорит
// словами и называет срок: человеку, у которого сорвалась связь, надо знать,
// что он не заблокирован навсегда.
func remoteRateLimitMiddleware(l *remoteRateLimiter, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			noteForwardedPresence(r, logger)
			key := clientRateKeyOpt(r, !l.ignoreForwarded)
			// Сначала ведёрко самого клиента: отказанный им же запрос общий
			// потолок не расходует, иначе один настойчивый клиент запер бы
			// всех, кто идёт через тот же прокси.
			ok, retry := l.Allow(key)
			if ok {
				if bok, bretry := l.backstop.Allow(r.RemoteAddr); !bok {
					l.refund(key)
					ok, retry = false, bretry
				}
			}
			if !ok {
				secs := int(retry.Round(time.Second).Seconds())
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				if logger != nil {
					logger.Warn("вход: слишком много попыток",
						"remote", key,
						"path", redactPathSecrets(r.URL.Path),
						"retry_after_sec", secs,
					)
				}
				writeJSONError(w, http.StatusTooManyRequests, "rate_limited",
					"Слишком много попыток. Попробуйте через "+strconv.Itoa(secs)+" с.")
				return
			}
			// Токен тратится ДО работы, поэтому залив неудачными запросами
			// упирается в лимит, не успев нагрузить проверку. Удачный вход
			// (код < 400) токен возвращает: подделать подпись нельзя, а
			// владелец не должен упираться в лимит своими заходами.
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 || rec.status < http.StatusBadRequest {
				l.refund(key)
			}
		})
	}
}
