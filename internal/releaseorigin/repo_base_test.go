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

func TestValidateReleaseTag(t *testing.T) {
	for _, in := range []string{"v0.13.0", "v0.13.0-rc132", "v1.2.3-hotfix_1"} {
		got, err := ValidateReleaseTag(" " + in + " ")
		if err != nil {
			t.Fatalf("ValidateReleaseTag(%q): %v", in, err)
		}
		if got != in {
			t.Fatalf("ValidateReleaseTag(%q)=%q", in, got)
		}
	}
	for _, bad := range []string{"", "0.13.0", "v", "v/../../x", "v0.13.0/evil", "v0.13.0?x=1", " v0.13.0 rc "} {
		if _, err := ValidateReleaseTag(bad); err == nil {
			t.Fatalf("ValidateReleaseTag(%q) unexpectedly succeeded", bad)
		}
	}
}
