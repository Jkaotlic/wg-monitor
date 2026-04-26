package backend

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type ctxKey int

const ctxKeyNickname ctxKey = iota

// AuthMiddleware returns middleware that requires `Authorization: Bearer <token>`.
// On match, the matched nickname is attached to the request context.
// Token comparison uses subtle.ConstantTimeCompare (timing-safe).
func AuthMiddleware(tokenToNickname map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || strings.HasPrefix(presented, " ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			for token, nick := range tokenToNickname {
				if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
					ctx := context.WithValue(r.Context(), ctxKeyNickname, nick)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// NicknameFromContext returns the agent nickname attached by AuthMiddleware,
// or empty string if not present.
func NicknameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyNickname).(string)
	return v
}
