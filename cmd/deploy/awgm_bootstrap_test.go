package main

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderAWGMBootstrapScriptContainsInstallPaths(t *testing.T) {
	rawToken := strings.Repeat("a", 64)
	script, err := RenderAWGMBootstrapScript(AWGMBootstrapParams{
		Nickname:     "testkeen",
		BackendURL:   "https://wg.example.test",
		RawToken:     rawToken,
		Version:      "v0.13.0-rc10",
		DownloadURL:  "https://example.test/wg-monitor-linux-arm64",
		ChecksumURL:  "https://example.test/checksums.txt",
		ChecksumName: "wg-monitor-linux-arm64",
	})
	if err != nil {
		t.Fatalf("RenderAWGMBootstrapScript: %v", err)
	}
	for _, want := range []string{"/opt/etc/wg-monitor/config.yaml", "/opt/bin/wg-monitor", "/opt/etc/init.d/S99wg-monitor", "agent:", "nickname: testkeen"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	for _, want := range []string{"existing_nickname", `sed 's/^"//; s/"$//'`, `sed "s/^'//; s/'$//"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing quoted nickname guard %q:\n%s", want, script)
		}
	}
	if !strings.Contains(script, `"$INIT" restart || "$INIT" start`) {
		t.Fatalf("normal bootstrap must start the agent service:\n%s", script)
	}
	if strings.Contains(script, "echo "+rawToken) {
		t.Fatal("script prints raw token")
	}
}

func TestRenderAWGMBootstrapScriptCanDeferServiceStart(t *testing.T) {
	script, err := RenderAWGMBootstrapScript(AWGMBootstrapParams{
		Nickname:     "client-g",
		BackendURL:   "https://wg.example.test",
		RawToken:     strings.Repeat("a", 64),
		Version:      "v0.13.0-rc59",
		DownloadURL:  "https://example.test/wg-monitor-linux-arm64",
		ChecksumURL:  "https://example.test/checksums.txt",
		ChecksumName: "wg-monitor-linux-arm64",
		DeferStart:   true,
	})
	if err != nil {
		t.Fatalf("RenderAWGMBootstrapScript: %v", err)
	}
	if strings.Contains(script, `"$INIT" restart || "$INIT" start`) {
		t.Fatalf("staged bootstrap must not start the agent before token_hash commit:\n%s", script)
	}
	if !strings.Contains(script, "service start deferred until backend token commit") {
		t.Fatalf("deferred bootstrap should explain that service start is delayed:\n%s", script)
	}
}

func TestRenderAWGMStartScriptRestartsService(t *testing.T) {
	script := RenderAWGMStartScript("client-g")
	for _, want := range []string{
		`INIT=/opt/etc/init.d/S99wg-monitor`,
		`"$INIT" restart || "$INIT" start`,
		`wg-monitor service started for`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start script missing %q:\n%s", want, script)
		}
	}
}

func TestRunAWGMStartAfterTokenCommitUsesSeparateStartScript(t *testing.T) {
	oldDirect := runAWGMBootstrapDirectFunc
	oldVPS := runAWGMBootstrapViaVPSFunc
	defer func() {
		runAWGMBootstrapDirectFunc = oldDirect
		runAWGMBootstrapViaVPSFunc = oldVPS
	}()

	var directScript string
	runAWGMBootstrapDirectFunc = func(_ *AWGMClient, script, terminalUser, terminalPass string) (TerminalRunResult, error) {
		if terminalUser != "root" || terminalPass != "secret" {
			t.Fatalf("terminal creds = %q/%q", terminalUser, terminalPass)
		}
		directScript = script
		return TerminalRunResult{Output: "ok"}, nil
	}
	runAWGMBootstrapViaVPSFunc = func(*State, *SecretStore, *AgentState, string, string, string, string, string, string) (TerminalRunResult, error) {
		t.Fatal("direct start must not call VPS runner")
		return TerminalRunResult{}, nil
	}

	res, err := runAWGMStartAfterTokenCommit(false, nil, nil, &AgentState{Nickname: "client-g"}, &AWGMClient{}, "", "", "", "root", "secret")
	if err != nil {
		t.Fatalf("runAWGMStartAfterTokenCommit: %v", err)
	}
	if res.Output != "ok" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(directScript, `"$INIT" restart || "$INIT" start`) {
		t.Fatalf("start script does not restart service:\n%s", directScript)
	}
	if strings.Contains(directScript, "bootstrap staged") || strings.Contains(directScript, "DOWNLOAD_URL=") {
		t.Fatalf("post-commit start must not rerun staged bootstrap:\n%s", directScript)
	}
}

func TestRunAWGMStartAfterTokenCommitPropagatesRunnerError(t *testing.T) {
	oldDirect := runAWGMBootstrapDirectFunc
	oldVPS := runAWGMBootstrapViaVPSFunc
	defer func() {
		runAWGMBootstrapDirectFunc = oldDirect
		runAWGMBootstrapViaVPSFunc = oldVPS
	}()

	wantErr := errors.New("terminal refused start")
	runAWGMBootstrapDirectFunc = func(*AWGMClient, string, string, string) (TerminalRunResult, error) {
		return TerminalRunResult{Output: "terminal output"}, wantErr
	}

	res, err := runAWGMStartAfterTokenCommit(false, nil, nil, &AgentState{Nickname: "client-g"}, &AWGMClient{}, "", "", "", "root", "secret")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if res.Output != "terminal output" {
		t.Fatalf("result output = %q", res.Output)
	}
}
