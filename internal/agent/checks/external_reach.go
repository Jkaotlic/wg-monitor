package checks

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// ExternalReachTarget — one HTTP probe target. URL must be a full HTTP/HTTPS
// URL; HEAD/GET semantics handled by the check.
type ExternalReachTarget struct {
	Name string
	URL  string
}

// ExternalReachCheck verifies that "blocked-in-RU" services are reachable
// via the WG path that owns the default route. Practical answer to
// "did anything actually break for the user?" — pingCheck on tunnel peers
// can be green while YouTube/Telegram/Instagram are dead, e.g. when RKN
// flips a regional filter or a CDN edge gets blackholed.
//
// Probes run in parallel (one HTTP client, multiple goroutines), each with
// its own per-probe timeout. FAIL when len(failed) >= FailThreshold.
type ExternalReachCheck struct {
	Targets         []ExternalReachTarget
	FailThreshold   int           // FAIL if failed >= threshold; 0 → ceil(N*2/3)
	HTTPClient      *http.Client  // iface-bound by caller; fallback to http.DefaultClient
	PerProbeTimeout time.Duration // default 5s
	ViaInterface    string        // informational; surfaced in Details for the renderer
}

func (ExternalReachCheck) Name() string { return "external_reach" }

func (c ExternalReachCheck) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.PerProbeTimeout <= 0 {
		c.PerProbeTimeout = 5 * time.Second
	}
	httpc := c.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}
	threshold := c.FailThreshold
	if threshold <= 0 {
		threshold = (len(c.Targets)*2 + 2) / 3
		if threshold < 1 {
			threshold = 1
		}
	}

	type result struct {
		ok  bool
		err string
	}
	results := make([]result, len(c.Targets))
	var wg sync.WaitGroup
	for i, t := range c.Targets {
		wg.Add(1)
		go func(i int, t ExternalReachTarget) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, c.PerProbeTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.URL, nil)
			if err != nil {
				results[i] = result{false, err.Error()}
				return
			}
			req.Header.Set("User-Agent", "wg-monitor/external-reach")
			resp, err := httpc.Do(req)
			if err != nil {
				results[i] = result{false, err.Error()}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 && resp.StatusCode/100 != 3 {
				results[i] = result{false, fmt.Sprintf("HTTP %d", resp.StatusCode)}
				return
			}
			results[i] = result{true, ""}
		}(i, t)
	}
	wg.Wait()

	var failed []map[string]any
	var okNames []string
	for i, r := range results {
		t := c.Targets[i]
		if r.ok {
			okNames = append(okNames, t.Name)
		} else {
			failed = append(failed, map[string]any{
				"name": t.Name,
				"url":  t.URL,
				"err":  r.err,
			})
		}
	}
	details := map[string]any{
		"targets_total":  len(c.Targets),
		"targets_failed": failed,
		"targets_ok":     okNames,
		"threshold":      threshold,
	}
	if c.ViaInterface != "" {
		details["via_interface"] = c.ViaInterface
	}
	if len(failed) >= threshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d/%d targets unreachable", len(failed), len(c.Targets)),
			details)
	}
	return OK(c.Name(), start, details)
}
