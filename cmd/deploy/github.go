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
	"strings"
	"time"
)

// RepoOwner and RepoName are set via -ldflags at build time so forks can rebuild
// without code changes:
//
//	-X main.RepoOwner=myname -X main.RepoName=wg-monitor-fork
var (
	RepoOwner = "anex"
	RepoName  = "wg-monitor"
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
	return filepath.Join(cache, "wg-monitor-deploy")
}

// GetLatestRelease calls https://api.github.com/repos/<owner>/<repo>/releases/latest.
func (d *Downloader) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", RepoOwner, RepoName)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wg-monitor-deploy/"+Version)
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
	return ParseRelease(body)
}

// GetAsset downloads and caches an asset, verifying its sha256 against checksumsURL.
// Returns the local cache path. Re-downloads if cached file's sha doesn't match.
func (d *Downloader) GetAsset(assetURL, assetName, checksumsURL, tag string) (string, error) {
	tagDir := filepath.Join(d.CacheDir, tag)
	if err := os.MkdirAll(tagDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(tagDir, assetName)

	wantSha, err := d.fetchExpectedSha(checksumsURL, assetName)
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

func (d *Downloader) fetchExpectedSha(checksumsURL, assetName string) (string, error) {
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
	_, err = io.Copy(f, resp.Body)
	return err
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
