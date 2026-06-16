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
		"https://evil.example/v1/releases/download",
		"https://github.com/Jkaotlic/wg-monitor/releases",
		"https://github.com/Jkaotlic/wg-monitor/releases/download-extra",
		"https://127.0.0.1/v1/releases/download",
		"https://127.0.0.1./v1/releases/download",
		"https://192.168.31.87/v1/releases/download",
		"https://192.168.31.87./v1/releases/download",
		"https://localhost/v1/releases/download",
		"https://localhost./v1/releases/download",
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

func TestValidateRepoBaseForBackendURLAllowsTrustedBackendMirror(t *testing.T) {
	allowed := []string{DefaultGitHubReleaseBase, DefaultBackendMirrorBase}
	got, err := ValidateRepoBaseForBackendURL("https://wg.example.test/v1/releases/download/", allowed, "https://wg.example.test")
	if err != nil {
		t.Fatalf("ValidateRepoBaseForBackendURL: %v", err)
	}
	if got != "https://wg.example.test/v1/releases/download" {
		t.Fatalf("repo_base=%q", got)
	}
}

func TestValidateRepoBaseForBackendURLRejectsForeignBackendMirror(t *testing.T) {
	allowed := []string{DefaultGitHubReleaseBase, DefaultBackendMirrorBase}
	_, err := ValidateRepoBaseForBackendURL("https://evil.example/v1/releases/download", allowed, "https://wg.example.test")
	if err == nil {
		t.Fatal("foreign backend mirror unexpectedly allowed")
	}
	if !strings.Contains(err.Error(), "repo_base") {
		t.Fatalf("error should mention repo_base, got %v", err)
	}
}

func TestBackendMirrorBaseForBackendURLRejectsPrivateBackend(t *testing.T) {
	if _, err := BackendMirrorBaseForBackendURL("https://192.168.31.87"); err == nil {
		t.Fatal("private backend mirror unexpectedly allowed")
	}
	if _, err := BackendMirrorBaseForBackendURL("https://192.168.31.87."); err == nil {
		t.Fatal("trailing-dot private backend mirror unexpectedly allowed")
	}
	if _, err := ValidateRepoBaseForBackendURL("https://192.168.31.87./v1/releases/download", nil, "https://192.168.31.87."); err == nil {
		t.Fatal("trailing-dot private repo_base unexpectedly allowed through trusted backend")
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
