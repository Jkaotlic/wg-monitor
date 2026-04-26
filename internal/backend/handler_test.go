package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func newServer(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	tokens := map[string]string{
		"deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe": "testkeen",
	}
	return NewMux(logger, tokens), logBuf
}

func TestHealthz(t *testing.T) {
	h, _ := newServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body: %q", rec.Body.String())
	}
}

func TestReport_HappyPath(t *testing.T) {
	h, logBuf := newServer(t)
	report := wire.Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks: []wire.Check{
			{Name: "agent_heartbeat", Status: "ok", DurationMs: 1},
		},
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code: %d body: %s", rec.Code, rec.Body.String())
	}
	logged := logBuf.String()
	for _, want := range []string{`"nickname":"testkeen"`, `"agent_version":"0.1.0"`, `"check_count":1`} {
		if !strings.Contains(logged, want) {
			t.Errorf("log missing %s; full log: %s", want, logged)
		}
	}
}

func TestReport_RejectsBadJSON(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("POST", "/v1/report", io.NopCloser(strings.NewReader("not json")))
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code: %d want 400", rec.Code)
	}
}

func TestReport_Unauthorized(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("POST", "/v1/report", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code: %d", rec.Code)
	}
}

func TestReport_RejectsGet(t *testing.T) {
	h, _ := newServer(t)
	req := httptest.NewRequest("GET", "/v1/report", nil)
	req.Header.Set("Authorization", "Bearer deadbeefcafebabedeadbeefcafebabedeadbeefcafebabedeadbeefcafebabe")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code: %d want 405", rec.Code)
	}
}
