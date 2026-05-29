package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestConfirmAWGMInstallInputsLetsOperatorCorrectRouterFields(t *testing.T) {
	state := &State{Backend: BackendState{Domain: "wg.example.test"}}
	ag := &AgentState{
		Nickname: "client-b",
		Kind:     "static",
		AWGMURL:  "https://old.example.test",
	}
	inputs := awgmInstallInputs{TerminalUser: "root"}
	restore := withSlowTestStdin(t, []string{"url", "awg.good.example.test", "kind", "mobile", "arch", "mips", "ok"})
	defer restore()

	if !confirmAWGMInstallInputs(state, &SecretStore{}, ag, &inputs) {
		t.Fatal("review should continue after corrections")
	}
	if ag.AWGMURL != "https://awg.good.example.test" {
		t.Fatalf("AWGMURL=%q", ag.AWGMURL)
	}
	if ag.Kind != "mobile" {
		t.Fatalf("Kind=%q", ag.Kind)
	}
	if ag.Arch != "mipsle" {
		t.Fatalf("Arch=%q", ag.Arch)
	}
}

func withSlowTestStdin(t *testing.T, lines []string) func() {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, line := range lines {
			_, _ = w.WriteString(line + "\n")
			time.Sleep(20 * time.Millisecond)
		}
		_ = w.Close()
	}()
	return func() {
		<-done
		os.Stdin = old
		_ = r.Close()
	}
}

func TestChooseAWGMFailureActionAllowsDeferredDeploy(t *testing.T) {
	restore := withTestStdin(t, "d\n")
	defer restore()

	got := chooseAWGMFailureAction(fmt.Errorf("awgm vps relay failed rc=1: websocket closed"), true)
	if got != awgmFailureDefer {
		t.Fatalf("action=%q, want defer", got)
	}
}

func TestRunAWGMBootstrapWithDirectFallbackUsesDirectOnTransientVPSFailure(t *testing.T) {
	var calls []string
	res, viaVPS, err := runAWGMBootstrapWithDirectFallback(true, func() (TerminalRunResult, error) {
		calls = append(calls, "vps")
		return TerminalRunResult{Output: "edge timeout"}, fmt.Errorf("awgm vps relay failed rc=1: websocket closed")
	}, func() (TerminalRunResult, error) {
		calls = append(calls, "direct")
		return TerminalRunResult{Output: "direct ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if viaVPS {
		t.Fatal("fallback success should switch bootstrap path to direct")
	}
	if res.Output != "direct ok" {
		t.Fatalf("result=%+v", res)
	}
	if got := fmt.Sprint(calls); got != "[vps direct]" {
		t.Fatalf("calls=%s", got)
	}
}

func TestRunAWGMBootstrapWithDirectFallbackKeepsScriptErrorsOnVPS(t *testing.T) {
	var directCalled bool
	_, viaVPS, err := runAWGMBootstrapWithDirectFallback(true, func() (TerminalRunResult, error) {
		return TerminalRunResult{Output: "script failed"}, fmt.Errorf("awgm vps relay failed rc=1: bootstrap script failed: opkg missing")
	}, func() (TerminalRunResult, error) {
		directCalled = true
		return TerminalRunResult{Output: "direct ok"}, nil
	})
	if err == nil {
		t.Fatal("script failure should stay failed")
	}
	if !viaVPS {
		t.Fatal("non-transient script failure should keep VPS path for operator recovery")
	}
	if directCalled {
		t.Fatal("direct fallback must not run for bootstrap script failures")
	}
}

func TestShortAWGMErrorForOperatorTruncatesHTML(t *testing.T) {
	err := fmt.Errorf("awgm POST /api/auth/login: HTTP 503: <!DOCTYPE html><html><body><noscript>503</noscript></body></html>")
	got := shortAWGMErrorForOperator(err)
	if got != "awgm POST /api/auth/login: HTTP 503" {
		t.Fatalf("short error=%q", got)
	}
}
