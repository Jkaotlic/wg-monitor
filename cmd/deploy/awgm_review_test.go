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
