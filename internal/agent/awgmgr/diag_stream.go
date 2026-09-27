package awgmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrDiagStreamUnsupported means this awg-manager build doesn't serve
// /api/diagnostics/stream (the endpoint shipped in 2.12) — callers fall back
// to whatever /api/diagnostics/result already has on record.
var ErrDiagStreamUnsupported = errors.New("diagnostics stream unsupported")

// defaultDiagStatusPollInterval is how often DiagFresh polls
// /api/diagnostics/status once the SSE stream has dropped before a
// terminal event arrived. Tests shrink diagStatusPollInterval to run fast;
// production always uses the default.
const defaultDiagStatusPollInterval = 2 * time.Second

// diagStatusPollInterval is a var (not a const) so tests can shrink it.
var diagStatusPollInterval = defaultDiagStatusPollInterval

// DiagFresh triggers one full diagnostic pass with IncludeRestart=false — no
// VPN tunnel is stopped or started anywhere in the run — via
// GET /api/diagnostics/stream?restart=false, and blocks until that run
// finishes.
//
// The stream can legitimately stay open for several seconds while
// awg-manager works through its checks. c.HTTP normally carries a short
// DefaultTimeout (5s) that bounds an *entire* request including reading the
// body, so reusing it here would cut the stream mid-read. DiagFresh instead
// issues the request on a copy of c.HTTP with Timeout reset to 0 — ctx is
// then the only deadline, exactly as callers expect from a Client method.
//
// If the connection drops before a terminal "done"/"error" SSE event
// arrives, the run itself is unaffected: awg-manager executes it in
// context.Background(), so a dropped client never stops it. DiagFresh
// notices the drop and falls back to polling /api/diagnostics/status every
// diagStatusPollInterval until the run stops reporting "running".
//
// Return values:
//   - nil: a fresh pass ran to completion; the caller reads the finished
//     report via DiagResult.
//   - ErrDiagStreamUnsupported: this build has no stream endpoint (older
//     than awg-manager 2.12) — callers fall back to whatever DiagResult
//     already has cached.
//   - any other error: the run itself failed (DIAG_STREAM_ERROR: <message>)
//     or the stream request could not even be made (HTTP_NNN: … / a network
//     error) or ctx expired while waiting.
func (c *Client) DiagFresh(ctx context.Context) error {
	resp, err := c.authRetry(ctx, "GET diagnostics/stream", func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/diagnostics/stream?restart=false", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if ck := c.cookie(); ck != nil {
			req.AddCookie(ck)
		}
		return c.streamHTTPClient().Do(req)
	})
	if err != nil {
		return fmt.Errorf("awgmgr GET diagnostics/stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return ErrDiagStreamUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("HTTP_%d: awgmgr diagnostics/stream: %s", resp.StatusCode, snippet(body))
	}

	switch outcome, msg, sawEvent := readDiagStream(resp.Body); outcome {
	case diagStreamDone:
		return nil
	case diagStreamError:
		return fmt.Errorf("DIAG_STREAM_ERROR: %s", msg)
	default: // diagStreamCut
		return c.waitForDiagIdle(ctx, sawEvent)
	}
}

// streamHTTPClient returns an *http.Client sharing every setting of c.HTTP
// (transport, redirect policy, cookie jar) except Timeout, which is reset to
// 0 so a long-running SSE read is bounded only by ctx.
func (c *Client) streamHTTPClient() *http.Client {
	cp := *c.HTTP
	cp.Timeout = 0
	return &cp
}

type diagStreamOutcome int

const (
	diagStreamCut diagStreamOutcome = iota
	diagStreamDone
	diagStreamError
)

// readDiagStream reads SSE frames (event: <type>\ndata: <json>\n\n) until it
// sees a terminal "done" or "error" event, or the stream ends without one
// (diagStreamCut — the connection dropped, not the run: see DiagFresh).
//
// sawEvent -- поток успел отдать хотя бы одно событие: прогон этого вызова
// зарегистрирован (CHK-06).
func readDiagStream(body io.Reader) (outcome diagStreamOutcome, msg string, sawEvent bool) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			sawEvent = true
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			switch event {
			case "done":
				return diagStreamDone, "", true
			case "error":
				msg := data
				var payload struct {
					Message string `json:"message"`
				}
				if jerr := json.Unmarshal([]byte(data), &payload); jerr == nil && payload.Message != "" {
					msg = payload.Message
				}
				return diagStreamError, msg, true
			}
		}
	}
	return diagStreamCut, "", sawEvent
}

// waitForDiagIdle polls /api/diagnostics/status every diagStatusPollInterval
// until the run it reports is no longer "running", or ctx expires.
// awg-manager runs the diagnostic pass in context.Background(), independent
// of any client connection, so this is purely "has it finished yet?" — not a
// trigger.
//
// CHK-06: машина состояний awg-manager -- idle|running (живой openapi.yaml,
// DiagnosticsStatusData.status), отдельного "done" нет. Поэтому "idle"
// значит «прогон закончен», только если прогон этого вызова точно начался:
// поток успел отдать событие (started) или опрос видел "running". Иначе
// поток мог оборваться до регистрации прогона, и отчёт остался прежним.
func (c *Client) waitForDiagIdle(ctx context.Context, started bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(diagStatusPollInterval):
		}
		status, err := c.diagStatus(ctx)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "running":
			started = true
			continue
		case "done", "completed", "complete", "finished", "success":
			return nil
		case "error", "failed", "fail":
			return fmt.Errorf("DIAG_STREAM_ERROR: diagnostic run ended with status %q", status)
		case "idle":
			if started {
				return nil
			}
		}
		return fmt.Errorf("DIAG_UNCONFIRMED: diagnostic run completion not confirmed (status %q, run start not seen) — the last report may be stale", status)
	}
}

func (c *Client) diagStatus(ctx context.Context) (string, error) {
	var env Envelope[struct {
		Status string `json:"status"`
	}]
	if err := c.get(ctx, "/api/diagnostics/status", &env); err != nil {
		return "", fmt.Errorf("awgmgr GET diagnostics/status: %w", err)
	}
	return env.Data.Status, nil
}
