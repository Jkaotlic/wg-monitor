package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestClient_SendReport_RemembersHookReports(t *testing.T) {
	body := `{"hook_reports":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "v0.47.0", 2*time.Second)
	if c.HookReportsAllowed() {
		t.Fatal("до первого ответа -- нельзя")
	}
	if _, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !c.HookReportsAllowed() {
		t.Fatal("бэкенд объявил hook_reports")
	}
	body = "" // бэкенд откатили на v0.46: пустое тело
	if _, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if c.HookReportsAllowed() {
		t.Fatal("после отката бэкенда разрешение обязано сняться")
	}
}
