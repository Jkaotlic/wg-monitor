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
	ConfigReason    string
	ConfigError     string
}

func (ExternalReachCheck) Name() string { return "external_reach" }

func (c ExternalReachCheck) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.ConfigError != "" {
		reason := c.ConfigReason
		if reason == "" {
			reason = "config_error"
		}
		return Fail(c.Name(), start, c.ConfigError, map[string]any{
			"reason": reason,
			"error":  c.ConfigError,
		})
	}
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
		ok     bool
		err    string
		status int // HTTP status when a response arrived; 0 on transport error
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
				results[i] = result{ok: false, err: err.Error()}
				return
			}
			req.Header.Set("User-Agent", "wg-monitor/external-reach")
			resp, err := httpc.Do(req)
			if err != nil {
				results[i] = result{ok: false, err: err.Error()}
				return
			}
			defer resp.Body.Close()
			// We got an HTTP response, so the network path through the tunnel
			// actually worked (DNS → TCP → TLS → HTTP round-trip all succeeded).
			// Only a 5xx (server-side error) counts as "unreachable". A 4xx —
			// e.g. Instagram/YouTube answering our non-browser User-Agent with
			// 403/429 — proves the path is fine and the service deliberately
			// refused the bot. Counting those as failures produced false
			// "Внешние сервисы недоступны через туннель" HARD alerts; the real
			// RKN/blackhole failure modes this check exists to catch surface as
			// transport errors (timeout / refused / reset) or 5xx, both still
			// caught below.
			if resp.StatusCode >= 500 {
				results[i] = result{ok: false, err: fmt.Sprintf("HTTP %d", resp.StatusCode), status: resp.StatusCode}
				return
			}
			results[i] = result{ok: true, status: resp.StatusCode}
		}(i, t)
	}
	wg.Wait()

	// Three buckets: failed (transport error / 5xx — counts toward threshold),
	// degraded (reachable but the service answered 4xx, e.g. Instagram/YouTube
	// refusing our bot UA — path works, does NOT count as failure but is worth
	// surfacing so the operator isn't misled by "Работают: Instagram" when it
	// really returned 403), and clean ok (2xx/3xx).
	var failed []map[string]any
	var degraded []map[string]any
	var okNames []string
	for i, r := range results {
		t := c.Targets[i]
		switch {
		case !r.ok:
			failed = append(failed, map[string]any{
				"name": t.Name,
				"url":  t.URL,
				"err":  r.err,
			})
		case r.status >= 400:
			degraded = append(degraded, map[string]any{
				"name":   t.Name,
				"status": r.status,
			})
		default:
			okNames = append(okNames, t.Name)
		}
	}
	details := map[string]any{
		"targets_total":  len(c.Targets),
		"targets_failed": failed,
		"targets_ok":     okNames,
		"threshold":      threshold,
	}
	if len(degraded) > 0 {
		details["targets_degraded"] = degraded
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
