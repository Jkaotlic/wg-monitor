package main

import (
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
	if strings.Contains(script, "echo "+rawToken) {
		t.Fatal("script prints raw token")
	}
}
