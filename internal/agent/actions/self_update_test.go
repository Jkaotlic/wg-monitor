package actions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

const expectedMaxSelfUpdateArtifactSize = 64 << 20

func TestHTTPGetRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, expectedMaxSelfUpdateArtifactSize+1)
	}))
	defer srv.Close()

	_, err := httpGet(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected oversized response to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size-limit error, got: %v", err)
	}
}

func TestSelfUpdateSwapScriptRollsBackWhenNewBinaryDoesNotStayRunning(t *testing.T) {
	script := selfUpdateSwapScript("/opt/bin/wg-monitor")

	required := []string{
		"/opt/etc/init.d/S99wg-monitor start",
		"is_running()",
		"pidof wg-monitor >/dev/null 2>&1",
		"pgrep -x wg-monitor >/dev/null 2>&1",
		"ps 2>/dev/null | grep '[w]g-monitor' >/dev/null 2>&1",
		"if ! is_running; then",
		"mv /opt/bin/wg-monitor.bak /opt/bin/wg-monitor",
		"/opt/etc/init.d/S99wg-monitor start",
	}
	last := -1
	for _, want := range required {
		idx := strings.Index(script[last+1:], want)
		if idx < 0 {
			t.Fatalf("script missing %q after offset %d:\n%s", want, last, script)
		}
		last += idx + len(want)
	}
}

func TestSelfUpdateURLsCanUseBackendMirror(t *testing.T) {
	binURL, sumsURL := selfUpdateURLs("v0.13.0-rc18", "wg-monitor-agent-linux-arm64", "https://wg.example.test/v1/releases/download/")
	if binURL != "https://wg.example.test/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64" {
		t.Fatalf("binURL=%q", binURL)
	}
	if sumsURL != "https://wg.example.test/v1/releases/download/v0.13.0-rc18/checksums.txt" {
		t.Fatalf("sumsURL=%q", sumsURL)
	}
}
