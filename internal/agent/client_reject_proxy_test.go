package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TestClient_SendReport_ProxyPageIsNotBackendRejection: метка report-rejected
// откатывает свежее обновление. Её ставит только ответ самого бэкенда (JSON).
// HTML-страница прокси перед лежащим бэкендом (KeenDNS, страница входа) с
// кодом 4xx -- это отвал, а не отказ, и откатывать по ней нельзя.
func TestClient_SendReport_ProxyPageIsNotBackendRejection(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		contentType  string
		wantRejected bool
		wantUnauth   bool
	}{
		{"proxy 404 html", http.StatusNotFound, "text/html", false, false},
		{"proxy 401 html", http.StatusUnauthorized, "text/html", false, true},
		{"proxy 403 no type", http.StatusForbidden, "", false, true},
		{"backend 422 json", http.StatusUnprocessableEntity, "application/json; charset=utf-8", true, false},
		{"backend 401 json", http.StatusUnauthorized, "application/json; charset=utf-8", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`<html>nope</html>`))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "tok", "v0.46.0", 2*time.Second)
			_, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
			if err == nil {
				t.Fatal("want error")
			}
			if got := errors.Is(err, ErrReportRejected); got != tc.wantRejected {
				t.Errorf("ErrReportRejected = %v, want %v (%v)", got, tc.wantRejected, err)
			}
			if got := errors.Is(err, ErrUnauthorized); got != tc.wantUnauth {
				t.Errorf("ErrUnauthorized = %v, want %v (%v)", got, tc.wantUnauth, err)
			}
		})
	}
}
