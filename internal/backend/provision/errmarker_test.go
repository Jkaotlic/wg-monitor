package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// BACK-01: relay сообщает класс отказа структурной меткой __WG_ERR__ в
// stdout. Раньше подсказка искала подстроки в relayErr, а от DefaultRelay
// там только «exit status 1» — «root-пароль не подошёл» не выдавался никогда,
// и оживление повторяло вход со старым паролем.
//
// v0.62 T10: метка несёт нонс задания (`__WG_ERR__ <nonce> <code>`). relay
// гонит вывод терминала роутера в тот же stdout, и роутер мог напечатать
// `__WG_ERR__ root_auth_failed` сам -- оживление стёрло бы сохранённый
// root-пароль. Нонс знают только runner и relay.

// nonceRelay -- заглушка relay, которая, как настоящий, берёт err_nonce из
// задания: "{nonce}" в строках заменяется на него.
type nonceRelay struct {
	scriptedRelay
	mu      sync.Mutex
	gotJob  []byte
	gotNonc string
}

func (n *nonceRelay) run(ctx context.Context, relayPath string, jobJSON []byte, onLine func(string)) (int, error) {
	var job struct {
		ErrNonce string `json:"err_nonce"`
	}
	_ = json.Unmarshal(jobJSON, &job)
	n.mu.Lock()
	n.gotJob = append([]byte(nil), jobJSON...)
	n.gotNonc = job.ErrNonce
	n.mu.Unlock()
	inner := n.scriptedRelay
	inner.lines = nil
	for _, l := range n.scriptedRelay.lines {
		inner.lines = append(inner.lines, strings.ReplaceAll(l, "{nonce}", job.ErrNonce))
	}
	return inner.run(ctx, relayPath, jobJSON, onLine)
}

func runErrMarkerJob(t *testing.T, relay RelayFunc, budget time.Duration) Job {
	t.Helper()
	return runErrMarkerJobWith(t, relay, budget, []byte(`{"mode":"x","nickname":"router-errmarker"}`))
}

func runErrMarkerJobWith(t *testing.T, relay RelayFunc, budget time.Duration, jobJSON []byte) Job {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	deps := Deps{
		Store:    NewStore(),
		BaseCtx:  context.Background(),
		Relay:    relay,
		LastSeen: func(string) (time.Time, bool) { return clock.now(), true },
		Now:      clock.now,
	}
	id, err := deps.Start(StartReq{
		Kind:          KindProvision,
		Nickname:      "router-errmarker",
		JobJSON:       jobJSON,
		InstallBudget: budget,
		VerifyBudget:  time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return waitForTerminal(t, deps.Store, id, 5*time.Second)
}

func TestRunner_RelayErrMarker_RootAuth(t *testing.T) {
	relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{"\r__WG_ERR__ {nonce} root_auth_failed\r"}, rc: 1, err: errors.New("exit status 1")}}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if job.Hint != HintRootAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintRootAuthFailed)
	}
}

func TestRunner_RelayErrMarker_AuthFailed(t *testing.T) {
	relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{"__WG_ERR__ {nonce} auth_failed"}, rc: 1, err: errors.New("exit status 1")}}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.Hint != HintAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintAuthFailed)
	}
}

func TestRunner_RelayErrMarker_SessionActive(t *testing.T) {
	relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{"__WG_ERR__ {nonce} session_active"}, rc: 1, err: errors.New("exit status 1")}}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	want := terminalConnectHint(errors.New("session_active"))
	if job.Hint != want || job.Hint == terminalConnectHint(nil) {
		t.Fatalf("Hint = %q, want busy-terminal hint %q", job.Hint, want)
	}
}

func TestRunner_RelayErrMarker_TimeoutWins(t *testing.T) {
	relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{"__WG_ERR__ {nonce} root_auth_failed"}, hang: true}}
	job := runErrMarkerJob(t, relay.run, 50*time.Millisecond)
	if job.State != StateFailed {
		t.Fatalf("State = %q, want failed", job.State)
	}
	if job.Hint != hintFor(StepTerminalConnected, context.DeadlineExceeded) {
		t.Fatalf("Hint = %q, want timeout hint", job.Hint)
	}
}

func TestRunner_RelayErrMarker_IgnoredOnSuccess(t *testing.T) {
	relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{"__WG_ERR__ {nonce} root_auth_failed"}, rc: 0}}
	job := runErrMarkerJob(t, relay.run, 2*time.Second)
	if job.State == StateFailed {
		t.Fatalf("job failed on rc=0 with a stray marker: hint=%q", job.Hint)
	}
}

// Настоящий DefaultRelay: stderr слит со stdout, ошибка -- голый
// «exit status 1». Метка обязана дойти до подсказки именно по этому пути.
func TestDefaultRelay_ErrMarker(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	script := filepath.Join(t.TempDir(), "fake-relay.py")
	body := "import json, sys\n" +
		"nonce = json.load(open(sys.argv[1]))['err_nonce']\n" +
		"print('__WG_ERR__ ' + nonce + ' root_auth_failed', flush=True)\n" +
		"print('root_auth_failed: router terminal refused the root login', file=sys.stderr)\n" +
		"sys.exit(1)\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	relay := func(ctx context.Context, _ string, jobJSON []byte, onLine func(string)) (int, error) {
		return DefaultRelay(ctx, script, jobJSON, onLine)
	}
	job := runErrMarkerJob(t, relay, 10*time.Second)
	if job.Hint != HintRootAuthFailed {
		t.Fatalf("Hint = %q, want %q", job.Hint, HintRootAuthFailed)
	}
}

// Вся цепочка: runner кладёт нонс -> настоящий встроенный awgm-relay.py
// падает на 401 панели -> метка с нонсом -> подсказка про вход в awg-manager.
func TestDefaultRelay_EmbeddedRelay_NonceEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer panel.Close()
	job, _ := json.Marshal(map[string]string{"base_url": panel.URL, "login": "admin", "password": "secret"})
	relay := func(ctx context.Context, _ string, jobJSON []byte, onLine func(string)) (int, error) {
		return DefaultRelay(ctx, "", jobJSON, onLine)
	}
	got := runErrMarkerJobWith(t, relay, 20*time.Second, job)
	if got.State != StateFailed || got.Hint != HintAuthFailed {
		t.Fatalf("State=%q Hint=%q, want failed + %q; tail=%q", got.State, got.Hint, HintAuthFailed, got.Tail)
	}
}

func TestParseErrLine(t *testing.T) {
	const n = "0123456789abcdef"
	cases := []struct {
		in    string
		nonce string
		code  string
		ok    bool
	}{
		{"__WG_ERR__ " + n + " root_auth_failed", n, "root_auth_failed", true},
		{"__WG_ERR__ " + n + " auth_failed", n, "auth_failed", true},
		{"__WG_ERR__ " + n + " session_active", n, "session_active", true},
		// Роутер печатает метку сам: без нонса, с чужим -- не код.
		{"__WG_ERR__ root_auth_failed", n, "", false},
		{"__WG_ERR__ fedcba9876543210 root_auth_failed", n, "", false},
		// Старый relay без нонса при пустом нонсе задания -- тоже не код.
		{"__WG_ERR__ root_auth_failed", "", "", false},
		{"__WG_ERR__  root_auth_failed", "", "", false},
		{"__WG_ERR__ " + n + " ", n, "", false},
		{"__WG_ERR__ " + n, n, "", false},
		{"__WG_ERR__ ", n, "", false},
		{"__WG_ERR__", n, "", false},
		{"__WG_ERR__ " + n + " something_else", n, "", false},
		{"__WG_ERR__ " + n + " root_auth_failed extra", n, "", false},
		{"echo __WG_ERR__ " + n + " root_auth_failed", n, "", false},
		{"__WG_STEP__ terminal_connected", n, "", false},
	}
	for _, c := range cases {
		code, ok := ParseErrLine(c.in, c.nonce)
		if code != c.code || ok != c.ok {
			t.Errorf("ParseErrLine(%q, %q) = (%q,%v), want (%q,%v)", c.in, c.nonce, code, ok, c.code, c.ok)
		}
	}
}

// Роутер в своём выводе печатает метку без нонса или с выдуманным -- runner
// её не принимает, подсказка общая (не «root-пароль не подошёл»), и
// оживление не стирает сохранённый пароль.
func TestRunner_RelayErrMarker_RouterForgedIgnored(t *testing.T) {
	for _, line := range []string{
		"__WG_ERR__ root_auth_failed",
		"__WG_ERR__ 0000000000000000 root_auth_failed",
	} {
		relay := &nonceRelay{scriptedRelay: scriptedRelay{lines: []string{line}, rc: 1, err: errors.New("exit status 1")}}
		job := runErrMarkerJob(t, relay.run, 2*time.Second)
		if job.State != StateFailed {
			t.Fatalf("%q: State = %q, want failed", line, job.State)
		}
		if job.Hint == HintRootAuthFailed {
			t.Fatalf("%q: forged marker produced HintRootAuthFailed", line)
		}
	}
}

// runner кладёт в задание err_nonce: 16 hex, своё на каждое задание, прочие
// поля задания не тронуты.
func TestRunner_JobJSONCarriesErrNonce(t *testing.T) {
	hex16 := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		relay := &nonceRelay{scriptedRelay: scriptedRelay{rc: 1, err: errors.New("exit status 1")}}
		runErrMarkerJob(t, relay.run, 2*time.Second)
		relay.mu.Lock()
		nonce, raw := relay.gotNonc, relay.gotJob
		relay.mu.Unlock()
		if !hex16.MatchString(nonce) {
			t.Fatalf("err_nonce = %q, want 16 hex", nonce)
		}
		if seen[nonce] {
			t.Fatalf("err_nonce %q repeated across jobs", nonce)
		}
		seen[nonce] = true
		var job map[string]any
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatalf("job JSON: %v", err)
		}
		if job["mode"] != "x" || job["nickname"] != "router-errmarker" {
			t.Fatalf("job fields lost: %s", raw)
		}
	}
}

// withErrNonce не трогает задание, которое не JSON-объект: нонс пустой,
// метки не принимаются, подсказка общая.
func TestWithErrNonce_NotAnObject(t *testing.T) {
	for _, in := range []string{"null", "[1]", "{not json", "", `"str"`, "42"} {
		out, nonce := withErrNonce([]byte(in))
		if nonce != "" {
			t.Errorf("withErrNonce(%q) nonce = %q, want empty", in, nonce)
		}
		if string(out) != in {
			t.Errorf("withErrNonce(%q) changed job to %q", in, out)
		}
	}
}
