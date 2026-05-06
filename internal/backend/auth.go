package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type ctxKey int

const (
	ctxKeyNickname ctxKey = iota
	ctxKeyUserID
)

type UserLookup interface {
	GetByToken(rawToken string) (*db.User, error)
}

func AuthMiddleware(lookup UserLookup) func(http.Handler) http.Handler {
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
			u, err := lookup.GetByToken(presented)
			if err != nil {
				if errors.Is(err, db.ErrUserNotFound) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
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
