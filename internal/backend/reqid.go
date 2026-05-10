package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// reqIDKey is the context key for the per-request id. Used by handlers and
// downstream goroutines so post-mortem can stitch logs across the ingest path
// (one report → 5 DB ops + N dispatcher.Handle + relay goroutines) — OBS-18.
type reqIDKeyType int

const reqIDKey reqIDKeyType = 0

// RequestIDFromContext returns the request id, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(reqIDKey).(string)
	return v
}

// requestIDMiddleware injects a fresh 8-byte hex request id into the ctx and
// echoes it as the X-Request-Id response header. Callers may also set their
// own X-Request-Id header on the inbound request — we honour it (capped to
// 64 chars) so wizard/curl-debugged calls can be traced end-to-end.
func requestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" || len(id) > 64 {
				id = newRequestID()
			}
			w.Header().Set("X-Request-Id", id)
			ctx := context.WithValue(r.Context(), reqIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
