package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCache_FetchAndCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v2.9.0"}})
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "awgmgr", GitHubRepo: "x/y"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	v, err := c.Latest(context.Background(), "awgmgr")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if v != "2.9.0" {
		t.Errorf("got %q want 2.9.0 (leading v should be stripped)", v)
	}
	v2, err := c.Latest(context.Background(), "awgmgr")
	if err != nil {
		t.Fatal(err)
	}
	if v2 != "2.9.0" {
		t.Errorf("cached: got %q want 2.9.0", v2)
	}
	if hits != 1 {
		t.Errorf("expected exactly 1 GitHub hit (cache miss?), got %d", hits)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.0"}})
	}))
	defer srv.Close()
	c := NewCache(5*time.Millisecond, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	if _, err := c.Latest(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Latest(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("expected 2 hits after TTL expiry, got %d", hits)
	}
}

func TestCache_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	v, err := c.Latest(context.Background(), "x")
	if err == nil {
		t.Error("expected error for 404")
	}
	if v != "" {
		t.Errorf("expected empty version on error, got %q", v)
	}
}

func TestCache_EmptyReleasesArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	if _, err := c.Latest(context.Background(), "x"); err == nil {
		t.Error("expected error for empty releases array")
	}
}

func TestCache_RejectsOversizedGitHubResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		_, _ = w.Write([]byte(strings.Repeat(" ", (1<<20)+1)))
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	_, err := c.Latest(context.Background(), "x")
	if err == nil {
		t.Fatal("expected oversized response to fail")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too large error, got %v", err)
	}
}

func TestCache_UnknownSource(t *testing.T) {
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	if _, err := c.Latest(context.Background(), "y"); err == nil {
		t.Error("expected error for unknown source")
	}
}

func TestCache_LatestAll_SnapshotOnly(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v3.0"}})
	}))
	defer srv.Close()
	c := NewCache(1*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=1"
	// LatestAll on a fresh cache returns empty.
	if got := c.LatestAll(); len(got) != 0 {
		t.Errorf("LatestAll should be empty before any Latest calls, got %v", got)
	}
	if _, err := c.Latest(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	got := c.LatestAll()
	if e, ok := got["x"]; !ok || e.Latest != "3.0" {
		t.Errorf("LatestAll missing or wrong: %+v", got)
	}
	if hits != 1 {
		t.Errorf("LatestAll must NOT trigger fetch; hits=%d", hits)
	}
}

// BACK-02: закрытая вкладка (отменённый ctx) не должна ни срывать запрос в
// GitHub, ни класть ошибку в кэш на весь TTL.
func TestCache_CancelledCallerCtxStillFetchesAndCaches(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v3.1.0"}})
	}))
	defer srv.Close()
	c := NewCache(12*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=10"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, err := c.Latest(ctx, "x")
	if err != nil || v != "3.1.0" {
		t.Fatalf("отменённый ctx сорвал запрос: v=%q err=%v", v, err)
	}
	v, err = c.Latest(context.Background(), "x")
	if err != nil || v != "3.1.0" || hits != 1 {
		t.Fatalf("результат не закэширован: v=%q err=%v hits=%d", v, err, hits)
	}
}

// BACK-02: ошибка апстрима живёт в кэше negativeTTL (5 мин), а не 12 часов.
func TestCache_ErrorCachedOnlyNegativeTTL(t *testing.T) {
	hits := 0
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if fail {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.2.3"}})
	}))
	defer srv.Close()
	c := NewCache(12*time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=10"
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	if _, err := c.Latest(context.Background(), "x"); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	clock = clock.Add(4*time.Minute + 59*time.Second)
	if _, err := c.Latest(context.Background(), "x"); err == nil || hits != 1 {
		t.Fatalf("до 5 мин ошибка должна браться из кэша: err=%v hits=%d", err, hits)
	}
	fail = false
	clock = clock.Add(2 * time.Second) // 5 мин 1 с от начала
	v, err := c.Latest(context.Background(), "x")
	if err != nil || v != "1.2.3" || hits != 2 {
		t.Fatalf("через 5 мин 1 с нужен новый запрос: v=%q err=%v hits=%d", v, err, hits)
	}
}

// BACK-08: pre-release и черновик не считаются «последней версией».
func TestCache_SkipsPrereleaseAndDraft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v2.21.0", "draft": true},
			{"tag_name": "v2.20.0-rc1", "prerelease": true},
			{"tag_name": "v2.19.16"},
		})
	}))
	defer srv.Close()
	c := NewCache(time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=10"
	v, err := c.Latest(context.Background(), "x")
	if err != nil || v != "2.19.16" {
		t.Fatalf("got %q err=%v, want 2.19.16", v, err)
	}
}

func TestCache_OnlyPrereleasesIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.0.0-rc1", "prerelease": true}})
	}))
	defer srv.Close()
	c := NewCache(time.Hour, []Source{{Name: "x", GitHubRepo: "a/b"}})
	c.api = srv.URL + "/repos/%s/releases?per_page=10"
	if _, err := c.Latest(context.Background(), "x"); err == nil {
		t.Fatal("ожидалась ошибка: стабильных релизов нет")
	}
}
