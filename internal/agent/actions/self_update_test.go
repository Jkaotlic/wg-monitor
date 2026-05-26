package actions

import (
	"context"
	"errors"
	"io"
	"net"
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

func TestSelfUpdateSwapCommandDoesNotRequireNohup(t *testing.T) {
	cmd := selfUpdateSwapCommand("/tmp/wg-monitor-swap.sh")
	got := strings.Join(cmd.Args, " ")
	if strings.Contains(got, "nohup") {
		t.Fatalf("swap command must not depend on nohup: %q", got)
	}
	if got != "sh /tmp/wg-monitor-swap.sh" {
		t.Fatalf("swap command=%q", got)
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

func TestHTTPGetWithFallbackRetriesNetworkFailureOnly(t *testing.T) {
	primary := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`dial tcp: lookup wgmonitor.jkaotlic.duckdns.org on 127.0.0.1:53: i/o timeout`)
	})}
	fallbackHit := false
	fallback := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		fallbackHit = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	body, err := httpGetWithFallback(context.Background(), primary, fallback, "https://wg.example.test/file", "83.171.224.125")
	if err != nil {
		t.Fatalf("httpGetWithFallback: %v", err)
	}
	if string(body) != "ok" || !fallbackHit {
		t.Fatalf("fallback not used correctly: body=%q hit=%v", body, fallbackHit)
	}
}

func TestHTTPGetWithFallbackDoesNotRetryHTTPFailure(t *testing.T) {
	primary := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("missing")),
			Header:     make(http.Header),
		}, nil
	})}
	fallback := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("fallback must not run for HTTP errors")
		return nil, nil
	})}

	_, err := httpGetWithFallback(context.Background(), primary, fallback, "https://wg.example.test/file", "83.171.224.125")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected original HTTP 404, got %v", err)
	}
}

func TestHTTPClientForPinnedRepoHostDialsResolvedIPForSameHost(t *testing.T) {
	var dialAddr string
	c, label := httpClientForPinnedRepoHost("https://wgmonitor.jkaotlic.duckdns.org/v1/releases/download", "83.171.224.125", 5, func(network, addr string) (net.Conn, error) {
		dialAddr = addr
		return nil, errors.New("stop after dial capture")
	})
	if c == nil || label != "83.171.224.125" {
		t.Fatalf("client=%v label=%q", c, label)
	}
	_, _ = httpGet(context.Background(), c, "https://wgmonitor.jkaotlic.duckdns.org/v1/releases/download/v/tag")
	if dialAddr != "83.171.224.125:443" {
		t.Fatalf("dialAddr=%q", dialAddr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
