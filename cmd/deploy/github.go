package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/releasesig"
)

// RepoOwner and RepoName point at the GitHub repository the wizard pulls
// release artefacts from. CI sets these via -ldflags at build time:
//
//	-X main.RepoOwner=${{ github.repository_owner }}
//	-X main.RepoName=${{ github.event.repository.name }}
//
// Forks rebuilding locally can override the same way. The defaults below
// match the canonical upstream so a fresh `go build ./cmd/deploy` from
// the upstream tree without ldflags still works.
var (
	RepoOwner     = "Jkaotlic"
	RepoName      = "wg-monitor"
	GitHubAPIBase = "https://api.github.com"
)

type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

func (r *Release) AssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func ParseRelease(data []byte) (*Release, error) {
	var r Release
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

type Downloader struct {
	HTTP     *http.Client
	CacheDir string
}

func NewDownloader() *Downloader {
	return &Downloader{
		HTTP:     &http.Client{Timeout: 60 * time.Second},
		CacheDir: defaultCacheDir(),
	}
}

func defaultCacheDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	base := filepath.Join(cache, "wg-monitor-deploy")
	if p := currentProfile(); p != "" && p != "default" {
		return filepath.Join(base, "profiles", p)
	}
	return base
}

// GetLatestRelease returns the most recent published release, including
// prereleases. The /releases/latest endpoint skips prereleases entirely
// (returns 404 if every release is a prerelease), which is the wrong
// semantic for an RC-driven project — we always want "the freshest tag",
// rc or not. So we hit /releases (which lists all, newest first) and
// take element 0.
func (d *Downloader) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100", strings.TrimRight(GitHubAPIBase, "/"), RepoOwner, RepoName)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var list []Release
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no releases published yet for %s/%s", RepoOwner, RepoName)
	}
	rel := newestReleaseByTag(list)
	if shouldPreferRunningRelease(rel.TagName, Version) {
		if current, err := d.getReleaseByTag(Version); err == nil {
			return current, nil
		}
	}
	return &rel, nil
}

func newestReleaseByTag(list []Release) Release {
	best := list[0]
	for _, rel := range list[1:] {
		if releaseNewerForOperator(rel, best) {
			best = rel
		}
	}
	return best
}

func releaseNewerForOperator(a, b Release) bool {
	if !a.PublishedAt.IsZero() || !b.PublishedAt.IsZero() {
		if a.PublishedAt.After(b.PublishedAt) {
			return true
		}
		if a.PublishedAt.Before(b.PublishedAt) {
			return false
		}
	}
	return compareReleaseTags(a.TagName, b.TagName) > 0
}

func (d *Downloader) getReleaseByTag(tag string) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
		strings.TrimRight(GitHubAPIBase, "/"), RepoOwner, RepoName, tag)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API tag %s: %w", tag, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API tag %s HTTP %d: %s", tag, resp.StatusCode, string(body))
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release %s: %w", tag, err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release %s has empty tag_name", tag)
	}
	return &rel, nil
}

func shouldPreferRunningRelease(latestTag, runningTag string) bool {
	if !strings.HasPrefix(runningTag, "v") {
		return false
	}
	latestRank, latestOK := parseReleaseTagRank(latestTag)
	runningRank, runningOK := parseReleaseTagRank(runningTag)
	if latestOK && runningOK {
		for i := 0; i < 3; i++ {
			if runningRank[i] > latestRank[i] {
				return true
			}
			if runningRank[i] < latestRank[i] {
				return false
			}
		}
		runningIsRC := strings.Contains(runningTag, "-rc")
		latestIsRC := strings.Contains(latestTag, "-rc")
		return runningIsRC && latestIsRC && runningRank[3] > latestRank[3]
	}
	return compareReleaseTags(runningTag, latestTag) > 0
}

func compareReleaseTags(a, b string) int {
	av, aok := parseReleaseTagRank(a)
	bv, bok := parseReleaseTagRank(b)
	if !aok || !bok {
		return strings.Compare(a, b)
	}
	for i := range av {
		if av[i] > bv[i] {
			return 1
		}
		if av[i] < bv[i] {
			return -1
		}
	}
	return 0
}

func parseReleaseTagRank(tag string) ([4]int, bool) {
	var rank [4]int
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	main, rcRaw, hasRC := strings.Cut(tag, "-rc")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return rank, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return rank, false
		}
		rank[i] = n
	}
	if !hasRC {
		rank[3] = 1 << 30
		return rank, true
	}
	rc, err := strconv.Atoi(rcRaw)
	if err != nil {
		return rank, false
	}
	rank[3] = rc
	return rank, true
}

// GetAsset downloads and caches an asset, verifying its sha256 against checksumsURL.
// Returns the local cache path. Re-downloads if cached file's sha doesn't match.
func (d *Downloader) GetAsset(assetURL, assetName, checksumsURL, tag string) (string, error) {
	tagDir := filepath.Join(d.CacheDir, tag)
	if err := os.MkdirAll(tagDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(tagDir, assetName)

	wantSha, err := d.fetchExpectedSha(checksumsURL, assetName, tag)
	if err != nil {
		return "", fmt.Errorf("checksums.txt: %w", err)
	}

	// Cache hit?
	if existing, err := os.ReadFile(target); err == nil {
		if hashHex(existing) == wantSha {
			return target, nil
		}
		os.Remove(target)
	}

	tmp := target + ".tmp"
	if err := d.downloadTo(assetURL, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	body, err := os.ReadFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	got := hashHex(body)
	if got != wantSha {
		os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", assetName, got, wantSha)
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return "", err
	}
	return target, nil
}

func (d *Downloader) fetchExpectedSha(checksumsURL, assetName, tag string) (string, error) {
	resp, err := d.HTTP.Get(checksumsURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if releasesig.SignatureRequiredForVersion(tag) {
		sigResp, err := d.HTTP.Get(checksumsURL + ".sig")
		if err != nil {
			return "", fmt.Errorf("signature: %w", err)
		}
		defer sigResp.Body.Close()
		if sigResp.StatusCode != 200 {
			return "", fmt.Errorf("signature: HTTP %d", sigResp.StatusCode)
		}
		sig, err := io.ReadAll(sigResp.Body)
		if err != nil {
			return "", fmt.Errorf("signature: %w", err)
		}
		if err := releasesig.VerifyChecksumsSignature(body, sig); err != nil {
			return "", fmt.Errorf("signature: %w", err)
		}
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// checksums.txt format: "<sha256>  <filename>"
		if f[1] == assetName || strings.HasSuffix(f[1], "/"+assetName) {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("asset %s not found in checksums", assetName)
}

func (d *Downloader) downloadTo(url, path string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Cap downloads at 200 MB so a poisoned CDN/release can't fill the
	// operator's disk before the SHA verify catches it (SEC-06). All current
	// release assets are ≤30 MB; 200 MB is a generous ceiling.
	const maxArtifactBytes = 200 << 20
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxArtifactBytes+1))
	if err != nil {
		return err
	}
	if n > maxArtifactBytes {
		return fmt.Errorf("download exceeded %d-byte cap (got >%d) for %s", maxArtifactBytes, maxArtifactBytes, url)
	}
	return nil
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
