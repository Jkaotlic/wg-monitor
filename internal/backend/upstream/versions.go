// Package upstream fetches latest released versions of external software
// (awg-manager, HydraRoute-Neo) from GitHub, caching responses to avoid
// burning the anonymous-API rate limit (60 req/h). Used by the backend to
// compute soft warnings in smart-reply and the maintenance panel.
//
// All methods are safe for concurrent use.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Source identifies one piece of upstream software. Name is the stable key
// callers use in Latest("awgmgr"); GitHubRepo is "<owner>/<name>".
type Source struct {
	Name       string
	GitHubRepo string
}

// Entry is one cached fetch result. Err is non-nil if the last fetch failed
// — Latest will surface that error to the caller and re-fetch on the next
// call after TTL expires.
type Entry struct {
	Latest    string
	FetchedAt time.Time
	Err       error
}

// Cache is a per-source TTL'd GitHub-releases fetcher. Construct via
// NewCache; safe for concurrent use.
type Cache struct {
	TTL     time.Duration
	sources map[string]Source
	http    *http.Client
	api     string // template; "%s" becomes the repo. Test-overridable.

	mu   sync.RWMutex
	data map[string]Entry
	// inflight[name] !=  nil means a fetch is in progress; subsequent
	// callers wait on the channel and read the cached entry instead of
	// firing concurrent GitHub requests. Anonymous-IP rate limit is
	// 60 req/h — three TG-button taps in quick succession used to burn
	// 3 of those for one repo (LOGIC-05).
	inflight map[string]chan struct{}
}

const defaultAPI = "https://api.github.com/repos/%s/releases?per_page=1"
const maxGitHubReleasesBodyBytes = 1 << 20

// NewCache builds a Cache wired to the public GitHub API with a 10s timeout
// per request. `sources` is the closed set of names Latest will accept.
func NewCache(ttl time.Duration, sources []Source) *Cache {
	m := make(map[string]Source, len(sources))
	for _, s := range sources {
		m[s.Name] = s
	}
	return &Cache{
		TTL:      ttl,
		sources:  m,
		http:     &http.Client{Timeout: 10 * time.Second},
		api:      defaultAPI,
		data:     make(map[string]Entry),
		inflight: make(map[string]chan struct{}),
	}
}

// Latest returns the latest tag (with leading "v"/"V" stripped) for the named
// source. Cached for TTL since last successful or failed fetch. Returns an
// error for unknown name, fetch failure, or empty releases list — callers
// should treat any error as "version unknown" and skip the warning UI rather
// than alarming the user.
func (c *Cache) Latest(ctx context.Context, name string) (string, error) {
	src, ok := c.sources[name]
	if !ok {
		return "", fmt.Errorf("unknown upstream source: %q", name)
	}
	for {
		c.mu.Lock()
		if e, ok := c.data[name]; ok && time.Since(e.FetchedAt) < c.TTL {
			c.mu.Unlock()
			return e.Latest, e.Err
		}
		// Already-fetching path: ждём окончания, потом возвращаем
		// свежий cached entry. Не дёргаем GitHub параллельно.
		if waitCh, busy := c.inflight[name]; busy {
			c.mu.Unlock()
			select {
			case <-waitCh:
				continue // re-check cache
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		// Acquire fetch slot.
		ch := make(chan struct{})
		c.inflight[name] = ch
		c.mu.Unlock()

		v, err := c.fetch(ctx, src.GitHubRepo)
		if err != nil {
			// Loud-fail: lets the operator notice expired GitHub tokens / repo
			// renames / 403 rate-limit before "no update warnings" mystery
			// (OBS-15). Hit at most once per TTL since the entry caches the err.
			slog.Warn("upstream fetch failed", "source", name, "repo", src.GitHubRepo, "err", err)
		}

		c.mu.Lock()
		c.data[name] = Entry{Latest: v, Err: err, FetchedAt: time.Now()}
		delete(c.inflight, name)
		c.mu.Unlock()
		close(ch)
		return v, err
	}
}

// LatestAll returns a snapshot of all currently cached entries (cache-only —
// does not trigger any fetch). Useful for renderers that want to see the
// state of the cache without forcing freshness.
func (c *Cache) LatestAll() map[string]Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Entry, len(c.data))
	for k, v := range c.data {
		out[k] = v
	}
	return out
}

func (c *Cache) fetch(ctx context.Context, repo string) (string, error) {
	url := fmt.Sprintf(c.api, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github %s: HTTP %d", repo, resp.StatusCode)
	}
	body, err := readLimited(resp.Body, maxGitHubReleasesBodyBytes)
	if err != nil {
		return "", fmt.Errorf("github %s: %w", repo, err)
	}
	var arr []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return "", fmt.Errorf("decode releases for %s: %w", repo, err)
	}
	if len(arr) == 0 {
		return "", fmt.Errorf("no releases for %s", repo)
	}
	return strings.TrimPrefix(strings.TrimPrefix(arr[0].TagName, "V"), "v"), nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response too large: limit %d bytes", maxBytes)
	}
	return body, nil
}
