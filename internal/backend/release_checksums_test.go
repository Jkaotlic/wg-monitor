package backend

import "testing"

func TestParseReleaseChecksums(t *testing.T) {
	content := "ABCDEF0123  wg-monitor-agent-linux-arm64\n" +
		"99aa  wg-monitor-agent-linux-mipsle\n" +
		"\n" +
		"garbage-line-no-second-field\n"
	got := parseReleaseChecksums(content)
	if got["wg-monitor-agent-linux-arm64"] != "abcdef0123" {
		t.Fatalf("arm64 sha = %q, want lowercased abcdef0123", got["wg-monitor-agent-linux-arm64"])
	}
	if got["wg-monitor-agent-linux-mipsle"] != "99aa" {
		t.Fatalf("mipsle sha = %q", got["wg-monitor-agent-linux-mipsle"])
	}
	if _, ok := got["garbage-line-no-second-field"]; ok {
		t.Fatal("malformed line must be skipped")
	}
}
