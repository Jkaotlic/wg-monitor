package backend

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// miniappInitDataMaxAge bounds how old a Telegram-signed initData payload may
// be when a session is minted from it. This only gates the one-time session
// mint (POST /v1/miniapp/session); the resulting session cookie has its own,
// independent TTL (miniappSessionTTL).
const miniappInitDataMaxAge = 24 * time.Hour

type miniappInitDataUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// verifyInitData validates a Telegram Mini App initData string per
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
// and returns the embedded user. now is an injectable seam for deterministic tests.
func verifyInitData(raw, botToken string, now time.Time) (miniappInitDataUser, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return miniappInitDataUser{}, errors.New("init_data: malformed query string")
	}
	hash := values.Get("hash")
	if hash == "" {
		return miniappInitDataUser{}, errors.New("init_data: missing hash")
	}
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+values.Get(k))
	}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	checkMAC := hmac.New(sha256.New, secretKey)
	checkMAC.Write([]byte(dataCheckString))
	computed := hex.EncodeToString(checkMAC.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(computed), []byte(hash)) != 1 {
		return miniappInitDataUser{}, errors.New("init_data: signature mismatch")
	}

	authDateUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return miniappInitDataUser{}, errors.New("init_data: missing/invalid auth_date")
	}
	authDate := time.Unix(authDateUnix, 0).UTC()
	if now.Sub(authDate) > miniappInitDataMaxAge {
		return miniappInitDataUser{}, errors.New("init_data: stale auth_date")
	}
	if authDate.After(now.Add(1 * time.Minute)) {
		return miniappInitDataUser{}, errors.New("init_data: auth_date in the future")
	}

	userRaw := values.Get("user")
	if userRaw == "" {
		return miniappInitDataUser{}, errors.New("init_data: missing user")
	}
	var u miniappInitDataUser
	if err := json.Unmarshal([]byte(userRaw), &u); err != nil {
		return miniappInitDataUser{}, errors.New("init_data: malformed user json")
	}
	if u.ID == 0 {
		return miniappInitDataUser{}, errors.New("init_data: missing user id")
	}
	return u, nil
}

const (
	miniappSessionCookieName = "wg_miniapp_session"
	miniappSessionTTL        = 24 * time.Hour
)

var miniappNow = time.Now

// miniappSessionEpoch is a server-side secret mixed into every mini-app
// session HMAC, independent of the dashboard's own epoch (dashboard_handler.go)
// so rotating one never logs the other out. Reuses newDashboardSessionEpoch's
// generator (same package) rather than duplicating the crypto/rand plumbing.
var (
	miniappSessionEpochMu sync.RWMutex
	miniappSessionEpoch   = newDashboardSessionEpoch()
)

func currentMiniappSessionEpoch() string {
	miniappSessionEpochMu.RLock()
	defer miniappSessionEpochMu.RUnlock()
	return miniappSessionEpoch
}

// RotateMiniappSessions invalidates every outstanding mini-app session cookie.
func RotateMiniappSessions() {
	miniappSessionEpochMu.Lock()
	defer miniappSessionEpochMu.Unlock()
	miniappSessionEpoch = newDashboardSessionEpoch()
}

func miniappSessionValue(botToken string, telegramUserID int64, expiresAt time.Time) string {
	exp := strconv.FormatInt(expiresAt.Unix(), 10)
	uid := strconv.FormatInt(telegramUserID, 10)
	mac := hmac.New(sha256.New, []byte(botToken))
	_, _ = mac.Write([]byte("wg-monitor-miniapp-session-v1"))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(currentMiniappSessionEpoch()))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(uid))
	_, _ = mac.Write([]byte(":"))
	_, _ = mac.Write([]byte(exp))
	return "v1:" + uid + ":" + exp + ":" + hex.EncodeToString(mac.Sum(nil))
}

func miniappSessionCookie(r *http.Request, botToken string, telegramUserID int64) *http.Cookie {
	expiresAt := miniappNow().Add(miniappSessionTTL)
	return &http.Cookie{
		Name:     miniappSessionCookieName,
		Value:    miniappSessionValue(botToken, telegramUserID, expiresAt),
		Path:     "/",
		MaxAge:   int(miniappSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	}
}

func miniappSessionUserID(r *http.Request, botToken string) (int64, bool) {
	cookie, err := r.Cookie(miniappSessionCookieName)
	if err != nil || cookie.Value == "" {
		return 0, false
	}
	parts := strings.Split(cookie.Value, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return 0, false
	}
	telegramUserID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	expUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false
	}
	expiresAt := time.Unix(expUnix, 0).UTC()
	if !miniappNow().Before(expiresAt) {
		return 0, false
	}
	want := miniappSessionValue(botToken, telegramUserID, expiresAt)
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(want)) != 1 {
		return 0, false
	}
	return telegramUserID, true
}

type miniappCtxKey struct{}

func contextWithMiniappUser(ctx context.Context, telegramUserID int64) context.Context {
	return context.WithValue(ctx, miniappCtxKey{}, telegramUserID)
}

func miniappUserFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(miniappCtxKey{}).(int64)
	return v, ok
}

// miniappVia -- откуда пришла личность: подпись Telegram или браузерный вход
// в веб-управление. Права от этого не зависят -- по via решается только
// защита от межсайтовых запросов и что показать человеку.
type miniappVia string

const (
	miniappViaTelegram miniappVia = "telegram"
	miniappViaWeb      miniappVia = "web"
)

type miniappViaCtxKey struct{}

func miniappViaFromContext(ctx context.Context) miniappVia {
	if v, ok := ctx.Value(miniappViaCtxKey{}).(miniappVia); ok {
		return v
	}
	return miniappViaTelegram
}

// miniappIdentify -- кто пришёл на /v1/miniapp/*. Кука мини-аппа называет
// человека и потому главнее; кука дашборда -- это вход админа из браузера
// (веб-управление = тот же мини-апп), и человеком для неё служит админ из
// конфига. Bearer-токен дашборда сюда намеренно не пускается: браузеру он не
// нужен, а вечный токен в заголовке -- лишняя дверь.
func miniappIdentify(r *http.Request, botToken, dashboardToken string, adminID int64) (int64, miniappVia, string) {
	if uid, ok := miniappSessionUserID(r, botToken); ok {
		return uid, miniappViaTelegram, ""
	}
	if dashboardToken == "" || !dashboardSessionValid(r, dashboardToken) {
		return 0, "", "unauthorized"
	}
	if adminID == 0 {
		return 0, "", "admin_not_configured"
	}
	return adminID, miniappViaWeb, ""
}

// miniappWebJSONOnly: браузерная кука уходит с любым запросом к сайту, поэтому
// запись из браузера требует application/json -- его не умеет отправить ни
// межсайтовая форма, ни простой запрос без предварительной проверки CORS.
func miniappWebJSONOnly(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct) == "application/json"
}

// MiniAppAuthMiddleware gates /v1/miniapp/* (except the session-mint endpoint
// itself) behind a valid session cookie minted by miniappSessionHandler, or a
// dashboard session cookie (browser entry, identity = configured admin).
func MiniAppAuthMiddleware(botToken, dashboardToken string, adminID int64, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			telegramUserID, via, code := miniappIdentify(r, botToken, dashboardToken, adminID)
			if code == "admin_not_configured" {
				if logger != nil {
					logger.Warn("веб-управление: вход есть, админ в конфиге не задан", "path", r.URL.Path)
				}
				writeJSONError(w, http.StatusUnauthorized, code, "admin not configured")
				return
			}
			if code != "" {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
				return
			}
			if via == miniappViaWeb && !miniappWebJSONOnly(r) {
				writeJSONError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedCT, "expected application/json")
				return
			}
			ctx := contextWithMiniappUser(r.Context(), telegramUserID)
			ctx = context.WithValue(ctx, miniappViaCtxKey{}, via)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
