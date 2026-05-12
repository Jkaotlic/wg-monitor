package backend

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// WizardAuthMiddleware gates /v1/wizard/* endpoints with a constant-time
// compare against the loaded wizard token. Empty `expected` is a bug —
// callers must check cfg.Wizard.Token != "" BEFORE wiring this middleware
// (the route registration in NewMux enforces that).
func WizardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("wizard auth: rejected",
			"reason", reason, "remote", r.RemoteAddr, "path", r.URL.Path,
		)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				logReject(r, "missing-bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			presented := strings.TrimPrefix(hdr, prefix)
			if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
				logReject(r, "token-mismatch")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
