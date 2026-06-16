package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRelease(t *testing.T) {
	data, err := os.ReadFile("testdata/github_release.json")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParseRelease(data)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v0.9.0" {
		t.Errorf("tag = %q want v0.9.0", rel.TagName)
	}
	if a := rel.AssetByName("wg-monitor-agent-linux-arm64"); a == nil {
		t.Error("expected to find arm64 agent asset")
	}
	if a := rel.AssetByName("nope"); a != nil {
		t.Error("expected nil for missing asset")
	}
}

func TestGetLatestReleaseRejectsOversizedReleaseList(t *testing.T) {
	oldAPI := GitHubAPIBase
	t.Cleanup(func() {
		GitHubAPIBase = oldAPI
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte("["))
			_, _ = io.Copy(w, io.LimitReader(zeroReader{}, (4<<20)+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPIBase = srv.URL

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	_, err := dl.GetLatestRelease()
	if err == nil {
		t.Fatal("expected oversized release list to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too large error, got %v", err)
	}
}

func TestDownloadAsset_VerifySha(t *testing.T) {
	body := []byte("hello world binary")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	checksums := fmt.Sprintf("%s  testbin\n", want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/testbin":
			w.Write(body)
		case "/checksums.txt":
			w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	dl := &Downloader{
		HTTP:     srv.Client(),
		CacheDir: cache,
	}

	got, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, cache) {
		t.Errorf("path %q not under cache %q", got, cache)
	}
	data, _ := os.ReadFile(got)
	if string(data) != string(body) {
		t.Error("downloaded body mismatch")
	}

	// Second call hits cache.
	got2, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != got2 {
		t.Error("expected same cached path on second call")
	}
}

func TestDownloadAsset_BadSha(t *testing.T) {
	body := []byte("real")
	wrong := strings.Repeat("0", 64)
	checksums := wrong + "  testbin\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/testbin":
			w.Write(body)
		case "/checksums.txt":
			w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	dl := &Downloader{HTTP: srv.Client(), CacheDir: cache}
	_, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.9.0")
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error should mention sha256, got %v", err)
	}
	// Also verify nothing committed to cache.
	entries, _ := filepath.Glob(filepath.Join(cache, "**/testbin"))
	if len(entries) > 0 {
		t.Errorf("expected no leftover files in cache, got %v", entries)
	}
}

func TestFetchExpectedShaRejectsOversizedChecksums(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = io.Copy(w, io.LimitReader(zeroReader{}, (1<<20)+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	_, err := dl.fetchExpectedSha(srv.URL+"/checksums.txt", "testbin", "v0.13.0-rc127")
	if err == nil {
		t.Fatal("expected oversized checksums.txt to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too large error, got %v", err)
	}
}

func TestDownloadAssetRequiresSignatureForNewRelease(t *testing.T) {
	body := []byte("hello world binary")
	sum := sha256.Sum256(body)
	checksums := fmt.Sprintf("%s  testbin\n", hex.EncodeToString(sum[:]))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/testbin":
			_, _ = w.Write(body)
		case "/checksums.txt":
			_, _ = w.Write([]byte(checksums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	_, err := dl.GetAsset(srv.URL+"/testbin", "testbin", srv.URL+"/checksums.txt", "v0.13.0-rc128")
	if err == nil {
		t.Fatal("expected missing checksums signature to fail")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestGetLatestReleaseDoesNotDowngradeBelowRunningVersion(t *testing.T) {
	oldVersion := Version
	oldAPI := GitHubAPIBase
	Version = "v0.13.0-rc21"
	t.Cleanup(func() {
		Version = oldVersion
		GitHubAPIBase = oldAPI
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v0.13.0-rc20","assets":[]}]`))
		case "/repos/Jkaotlic/wg-monitor/releases/tags/v0.13.0-rc21":
			_, _ = w.Write([]byte(`{"tag_name":"v0.13.0-rc21","assets":[{"name":"checksums.txt","browser_download_url":"https://example.test/checksums.txt"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPIBase = srv.URL

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	rel, err := dl.GetLatestRelease()
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if rel.TagName != "v0.13.0-rc21" {
		t.Fatalf("latest tag=%q, want running version v0.13.0-rc21", rel.TagName)
	}
}

func TestGetLatestReleaseChoosesHighestTagWhenGitHubOrderIsStale(t *testing.T) {
	oldAPI := GitHubAPIBase
	t.Cleanup(func() {
		GitHubAPIBase = oldAPI
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"v0.13.0-rc99","assets":[]},
				{"tag_name":"v0.13.0-rc98","assets":[]},
				{"tag_name":"v0.13.0-rc101","assets":[]},
				{"tag_name":"v0.13.0-rc100","assets":[]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPIBase = srv.URL

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	rel, err := dl.GetLatestRelease()
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if rel.TagName != "v0.13.0-rc101" {
		t.Fatalf("latest tag=%q, want v0.13.0-rc101", rel.TagName)
	}
}

func TestGetLatestReleaseChoosesNewestPublishedRCOverStable(t *testing.T) {
	oldAPI := GitHubAPIBase
	t.Cleanup(func() {
		GitHubAPIBase = oldAPI
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"v0.13.0","published_at":"2026-06-08T10:00:00Z","assets":[]},
				{"tag_name":"v0.13.0-rc119","published_at":"2026-06-10T07:54:00Z","assets":[]},
				{"tag_name":"v0.13.0-rc120","published_at":"2026-06-10T09:26:00Z","assets":[]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPIBase = srv.URL

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	rel, err := dl.GetLatestRelease()
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if rel.TagName != "v0.13.0-rc120" {
		t.Fatalf("latest tag=%q, want v0.13.0-rc120", rel.TagName)
	}
}

func TestGetLatestReleaseDoesNotChooseLowerPublishedLater(t *testing.T) {
	oldAPI := GitHubAPIBase
	t.Cleanup(func() {
		GitHubAPIBase = oldAPI
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Jkaotlic/wg-monitor/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"v0.13.0-rc132","published_at":"2026-06-11T10:00:00Z","assets":[]},
				{"tag_name":"v0.13.0-rc131","published_at":"2026-06-12T10:00:00Z","assets":[]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	GitHubAPIBase = srv.URL

	dl := &Downloader{HTTP: srv.Client(), CacheDir: t.TempDir()}
	rel, err := dl.GetLatestRelease()
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if rel.TagName != "v0.13.0-rc132" {
		t.Fatalf("latest tag=%q, want v0.13.0-rc132", rel.TagName)
	}
}

func TestParseReleaseTagRankRejectsSignedComponents(t *testing.T) {
	cases := []string{
		"v0.13.0-rc-1",
		"v0.13.0-rc+1",
		"v0.-1.0",
		"v+0.13.0",
	}
	for _, tag := range cases {
		t.Run(tag, func(t *testing.T) {
			if _, ok := parseReleaseTagRank(tag); ok {
				t.Fatalf("parseReleaseTagRank(%q) ok=true, want false", tag)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
