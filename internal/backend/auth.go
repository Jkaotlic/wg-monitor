package backend

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type ctxKey int

const (
	ctxKeyNickname ctxKey = iota
	ctxKeyUserID
)

type UserLookup interface {
	GetByToken(rawToken string) (*db.User, error)
}

// AuthMiddleware: Bearer-token validation + audit logging for 401s.
// Каждое отклонение даёт slog.Warn с remote_addr и path — это аудит-канал
// для brute-force / случайно-неверных токенов. logger опциональный (nil
// допустим — middleware работает без аудит-лога, для тестов).
func AuthMiddleware(lookup UserLookup, logger *slog.Logger) func(http.Handler) http.Handler {
	logUnauthorized := func(r *http.Request, reason string, presentedLen int) {
		if logger == nil {
			return
		}
		logger.Warn("auth: unauthorized",
			"reason", reason,
			"remote", r.RemoteAddr,
			"path", r.URL.Path,
			"method", r.Method,
			"presented_len", presentedLen,
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				logUnauthorized(r, "missing-bearer", 0)
				incAuthReject()
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || strings.HasPrefix(presented, " ") {
				logUnauthorized(r, "empty-token", 0)
				incAuthReject()
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			u, err := lookup.GetByToken(presented)
			if err != nil {
				if errors.Is(err, db.ErrUserNotFound) {
					logUnauthorized(r, "token-not-found", len(presented))
					incAuthReject()
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if logger != nil {
					logger.Error("auth: lookup failed",
						"err", err, "remote", r.RemoteAddr, "path", r.URL.Path)
				}
				http.Error(w, "auth lookup failed", http.StatusInternalServerError)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyNickname, u.Nickname)
			ctx = context.WithValue(ctx, ctxKeyUserID, u.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func NicknameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyNickname).(string)
	return v
}

func UserIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(ctxKeyUserID).(int64)
	return v
}
