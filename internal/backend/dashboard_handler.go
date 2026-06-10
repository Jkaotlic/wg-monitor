package backend

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DashboardAuthMiddleware gates /dashboard and /v1/dashboard/* with the
// dashboard token loaded at startup. Empty expected tokens must never be wired.
func DashboardAuthMiddleware(expected string, logger *slog.Logger) func(http.Handler) http.Handler {
	logReject := func(r *http.Request, reason string) {
		if logger == nil {
			return
		}
		logger.Warn("dashboard auth: rejected",
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

func registerDashboardRoutes(mux *http.ServeMux, d Deps) {
	dashAuth := DashboardAuthMiddleware(d.DashboardToken, d.Logger)
	mux.Handle("GET /v1/dashboard/summary", requestIDMiddleware()(dashAuth(dashboardSummaryHandler(d))))
}

func dashboardSummaryHandler(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Status      string `json:"status"`
			Version     string `json:"version"`
			GeneratedAt string `json:"generated_at"`
		}{
			Status:      "ok",
			Version:     serverVersion,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
}
