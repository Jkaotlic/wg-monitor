package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/websocket"
)

func TestNewAWGMClientInsecureTLSIsExplicitOptIn(t *testing.T) {
	t.Setenv("AWGM_INSECURE_TLS", "")
	strict := NewAWGMClient("https://awg.example", "", "")
	if strict.HTTP.Transport != nil {
		t.Fatalf("strict client transport = %#v, want default nil transport", strict.HTTP.Transport)
	}

	t.Setenv("AWGM_INSECURE_TLS", "1")
	insecure := NewAWGMClient("https://awg.example", "", "")
	tr, ok := insecure.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", insecure.HTTP.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("insecure TLS opt-in not reflected in transport config: %#v", tr.TLSClientConfig)
	}
	if _, ok := any(tr.TLSClientConfig).(*tls.Config); !ok {
		t.Fatalf("TLSClientConfig = %T, want *tls.Config", tr.TLSClientConfig)
	}
}

func TestAWGMClientLoginStoresSessionCookie(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "s1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"login":"admin"}`))
		case "/api/system/info":
			if ck, err := r.Cookie("awg_session"); err == nil && ck.Value == "s1" {
				sawCookie = true
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"goArch":"arm64","routerIP":"192.168.1.1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "admin", "secret")
	c.HTTP = srv.Client()
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.SystemInfo(context.Background()); err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if !sawCookie {
		t.Fatal("session cookie not sent")
	}
}

func TestAWGMClientSendsBasicAuthToProtectedWebLayer(t *testing.T) {
	var sawLoginBasic bool
	var sawInfoBasic bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="awg"`)
			http.Error(w, "basic auth required", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/auth/login":
			sawLoginBasic = true
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "s1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/api/system/info":
			sawInfoBasic = true
			_, _ = w.Write([]byte(`{"success":true,"data":{"goArch":"arm64","routerIP":"192.168.1.1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "admin", "secret")
	c.HTTP = srv.Client()
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.SystemInfo(context.Background()); err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if !sawLoginBasic || !sawInfoBasic {
		t.Fatalf("basic auth not observed on login=%v info=%v", sawLoginBasic, sawInfoBasic)
	}
}

func TestAWGMClientAPIKeySkipsLoginAndSendsBearer(t *testing.T) {
	var sawLogin bool
	var sawBearer bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			sawLogin = true
			http.Error(w, "login should not be called", http.StatusInternalServerError)
		case "/api/system/info":
			if r.Header.Get("Authorization") == "Bearer api-secret" {
				sawBearer = true
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"goArch":"arm64","routerIP":"192.168.1.1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "admin", "secret").WithAPIKey("api-secret")
	c.HTTP = srv.Client()
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login with API key: %v", err)
	}
	if _, err := c.SystemInfo(context.Background()); err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	if sawLogin {
		t.Fatal("Login was called despite API key")
	}
	if !sawBearer {
		t.Fatal("Bearer API key was not sent")
	}
}

func TestAWGMClientHTTPErrorExposesStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "", "").WithAPIKey("bad-token")
	c.HTTP = srv.Client()
	_, err := c.SystemInfo(context.Background())
	if err == nil {
		t.Fatal("SystemInfo unexpectedly succeeded")
	}
	var httpErr *AWGMHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *AWGMHTTPError: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode=%d, want %d", httpErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestAWGMClientTerminalBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/terminal/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true,"sessionActive":true}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "", "")
	c.HTTP = srv.Client()
	st, err := c.TerminalStatus(context.Background())
	if err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if !st.SessionActive {
		t.Fatalf("expected busy terminal: %+v", st)
	}
}

func TestAWGMClientTerminalStatusUsesAPIPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":false,"sessionActive":false}}`))
	}))
	defer srv.Close()

	c := NewAWGMClient(srv.URL, "", "")
	c.HTTP = srv.Client()
	if _, err := c.TerminalStatus(context.Background()); err != nil {
		t.Fatalf("TerminalStatus: %v", err)
	}
	if gotPath != "/api/terminal/status" {
		t.Fatalf("terminal status path = %q, want /api/terminal/status", gotPath)
	}
}

func TestAWGMClientTerminalWSConfigMatchesBrowser(t *testing.T) {
	c := NewAWGMClient("https://awg.example", "", "")
	wsURL, err := c.wsURL("/api/terminal/ws")
	if err != nil {
		t.Fatalf("wsURL: %v", err)
	}
	cfg, err := websocket.NewConfig(wsURL, c.BaseURL)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if len(cfg.Protocol) != 0 {
		t.Fatalf("unexpected websocket subprotocols: %v", cfg.Protocol)
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		t.Fatalf("parse wsURL: %v", err)
	}
	if u.Scheme != "wss" || u.Path != "/api/terminal/ws" {
		t.Fatalf("wsURL = %s, want wss /api/terminal/ws", wsURL)
	}
}

func TestAWGMTerminalPromptAction(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "login", text: "Keenetic login: ", want: "login"},
		{name: "password", text: "Password: ", want: "password"},
		{name: "combined-prefers-password", text: "Keenetic login: root\nPassword: ", want: "password"},
		{name: "shell-root", text: "root@keenetic:~# ", want: "shell"},
		{name: "shell-dollar", text: "admin@host:~$ ", want: "shell"},
		{name: "unknown", text: "Welcome\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := awgmTerminalPromptAction(tt.text); got != tt.want {
				t.Fatalf("awgmTerminalPromptAction(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestAWGMTerminalMarkerUsesLastOccurrenceInEchoedChunk(t *testing.T) {
	const marker = "__WG_MONITOR_DONE__"
	text := "cat <<EOF\necho " + marker + "$rc\nEOF\nreal output\n" + marker + "1\n"
	done, err := awgmTerminalDoneFromChunk(text, marker)
	if !done {
		t.Fatal("expected marker to be found")
	}
	if err == nil || !strings.Contains(err.Error(), "bootstrap script failed: 1") {
		t.Fatalf("error = %v, want rc=1 from last marker occurrence", err)
	}
}

func TestNormalizeAWGMURLDefaultsToHTTPS(t *testing.T) {
	if got := normalizeAWGMURL("awg.test.example/"); got != "https://awg.test.example" {
		t.Fatalf("normalizeAWGMURL without scheme = %q", got)
	}
	if got := normalizeAWGMURL("http://127.0.0.1:2222/"); got != "http://127.0.0.1:2222" {
		t.Fatalf("normalizeAWGMURL with scheme = %q", got)
	}
}

func TestAWGMBootstrapRelayURLDecision(t *testing.T) {
	state := &State{Backend: BackendState{Host: "83.171.224.125"}}
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "public keendns", url: "https://awg.example.com", want: true},
		{name: "private lan", url: "http://192.168.31.1:2222", want: false},
		{name: "localhost", url: "http://localhost:2222", want: false},
		{name: "loopback", url: "127.0.0.1:2222", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunAWGMBootstrapViaVPS(state, tt.url); got != tt.want {
				t.Fatalf("shouldRunAWGMBootstrapViaVPS(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
	if shouldRunAWGMBootstrapViaVPS(&State{}, "https://awg.example.com") {
		t.Fatal("relay should stay disabled without backend SSH host")
	}
}

func TestAWGMPreflightFallbackAllowsDirectForVPSWebEdgeErrors(t *testing.T) {
	for _, msg := range []string{
		"awgm vps system info failed rc=1: awgm GET /api/system/info: HTTP 403: forbidden",
		"awgm vps system info failed rc=1: awgm POST /api/auth/login: HTTP 502: bad gateway",
		"awgm vps relay ssh transport: context deadline exceeded",
	} {
		if !shouldTryDirectAWGMPreflightFallback(errString(msg)) {
			t.Fatalf("expected direct fallback for %q", msg)
		}
	}
}

func TestAWGMPreflightFallbackRejectsUnauthorized(t *testing.T) {
	for _, msg := range []string{
		"awgm vps system info failed rc=1: awgm GET /api/system/info: HTTP 401: unauthorized",
		"awgm vps system info failed rc=1: unauthorized",
	} {
		if shouldTryDirectAWGMPreflightFallback(errString(msg)) {
			t.Fatalf("401/unauthorized must not fall back silently: %q", msg)
		}
	}
}

func TestAWGMPreflightDirectFallbackSwitchesBootstrapPath(t *testing.T) {
	var vpsCalls, directCalls int
	info, useVPSBootstrap, err := probeAWGMSystemInfoWithDirectFallback(true, func() (*AWGMSystemInfo, error) {
		vpsCalls++
		return nil, errString("awgm vps system info failed rc=1: awgm GET /api/system/info: HTTP 403: forbidden")
	}, func() (*AWGMSystemInfo, error) {
		directCalls++
		return &AWGMSystemInfo{GoArch: "arm64", RouterIP: "192.168.31.1"}, nil
	})
	if err != nil {
		t.Fatalf("probeAWGMSystemInfoWithDirectFallback: %v", err)
	}
	if info == nil || info.GoArch != "arm64" {
		t.Fatalf("fallback info not returned: %+v", info)
	}
	if vpsCalls != 1 || directCalls != 1 {
		t.Fatalf("probe counts vps=%d direct=%d, want 1/1", vpsCalls, directCalls)
	}
	if useVPSBootstrap {
		t.Fatal("successful direct fallback must make terminal bootstrap use direct path")
	}
}

func TestParseAWGMSystemInfoRelayOutput(t *testing.T) {
	out := "noise\n__WG_MONITOR_JSON__{\"success\":true,\"data\":{\"goArch\":\"arm64\",\"routerIP\":\"192.168.1.1\",\"version\":\"1.2.3\"}}\n"
	info, err := parseAWGMSystemInfoRelayOutput(out)
	if err != nil {
		t.Fatalf("parse relay output: %v", err)
	}
	if info.GoArch != "arm64" || info.RouterIP != "192.168.1.1" || info.Version != "1.2.3" {
		t.Fatalf("bad info: %+v", info)
	}
}
