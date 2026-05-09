package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
