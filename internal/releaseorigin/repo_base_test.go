package releaseorigin

import (
	"strings"
	"testing"
)

func TestValidateRepoBaseAllowsCanonicalOrigins(t *testing.T) {
	allowed := []string{DefaultGitHubReleaseBase, DefaultBackendMirrorBase}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "github releases",
			in:   DefaultGitHubReleaseBase + "/",
			want: DefaultGitHubReleaseBase,
		},
		{
			name: "backend mirror",
			in:   " " + DefaultBackendMirrorBase + " ",
			want: DefaultBackendMirrorBase,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateRepoBase(c.in, allowed)
			if err != nil {
				t.Fatalf("ValidateRepoBase(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("normalized base=%q, want %q", got, c.want)
			}
		})
	}
}

func TestValidateRepoBaseRejectsUntrustedOrigins(t *testing.T) {
	allowed := []string{DefaultGitHubReleaseBase, DefaultBackendMirrorBase}
	cases := []string{
		"http://evil.example/releases/download",
		"https://evil.example/releases/download",
		"https://github.com/Jkaotlic/wg-monitor/releases",
		"https://github.com/Jkaotlic/wg-monitor/releases/download-extra",
		"https://127.0.0.1/v1/releases/download",
		"https://192.168.31.87/v1/releases/download",
		"https://localhost/v1/releases/download",
		"not a url",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ValidateRepoBase(in, allowed)
			if err == nil {
				t.Fatalf("ValidateRepoBase(%q) unexpectedly succeeded", in)
			}
			if !strings.Contains(err.Error(), "repo_base") {
				t.Fatalf("error should mention repo_base, got %v", err)
			}
		})
	}
}
