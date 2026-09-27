package provision

import (
	"errors"
	"testing"
)

// REV-04: отказ входа root и отказ панели (401 -- сменили ключ) -- разные
// беды. Сохранённый пароль root стирается только по первой, поэтому у неё
// своя подсказка; текст панельной (HintAuthFailed) видит дашборд -- прежний.
func TestTerminalConnectHint_RootLoginIsDistinctFromPanel401(t *testing.T) {
	for _, msg := range []string{"awgm terminal root login failed: Login incorrect", "Login incorrect", "root_auth_failed"} {
		if got := terminalConnectHint(errors.New(msg)); got != HintRootAuthFailed {
			t.Errorf("%q -> %q, хотим HintRootAuthFailed", msg, got)
		}
	}
	for _, msg := range []string{"awgm GET /api/terminal/status: HTTP 401: nope", "awgm login: success=false", "unauthorized"} {
		if got := terminalConnectHint(errors.New(msg)); got != HintAuthFailed {
			t.Errorf("%q -> %q, хотим HintAuthFailed (панель)", msg, got)
		}
	}
	if HintRootAuthFailed == HintAuthFailed {
		t.Fatal("подсказки обязаны различаться")
	}
}
