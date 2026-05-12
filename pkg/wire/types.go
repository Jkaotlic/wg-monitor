// Package wire defines the JSON wire format shared by agent and backend.
// Field tags here are the contract — changing them is a breaking change.
package wire

import (
	"encoding/json"
	"time"
)

type Report struct {
	Timestamp    time.Time `json:"ts"`
	AgentVersion string    `json:"agent_version"`
	Checks       []Check   `json:"checks"`
	// Resumed is true when this report follows a gap larger than the agent's
	// resumed-threshold (e.g. mobile router back online). Backend uses it to
	// suppress false ROUTER_OFFLINE alerts and grant a grace window for the
	// freshly-collected check results to populate the FSM.
	Resumed bool `json:"resumed,omitempty"`
}

type Check struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"duration_ms"`
	Details    map[string]any `json:"details,omitempty"`
}

// Command is a single instruction the backend dispatches to an agent over
// long-poll /v1/cmd. ID is opaque (UUID-ish) — the agent reports the same
// ID back via /v1/cmd/result so the backend can correlate outcome with the
// original TG callback that enqueued it.
type Command struct {
	ID       string         `json:"id"`
	Action   string         `json:"action"`
	Args     map[string]any `json:"args,omitempty"`
	IssuedAt time.Time      `json:"issued_at"`
}

var validCommandActions = map[string]bool{
	"restart_tunnel":    true,
	"diag_now":          true,
	"pingcheck_now":     true,
	"opkg_upgrade":      true,
	"opkg_feed_disable": true,
	"force_recheck":     true,
	"tunnel_enable":     true,
	"tunnel_disable":    true,
	"check_via_tunnel":  true,
	"check_direct":      true,
	"tunnel_import":     true,
	"route_status":      true,
	"route_rebind":      true,
	"service_restart":   true,
	"firmware_status":   true,
	"firmware_install":  true,
	"version_audit":     true,
}

func IsValidCommandAction(a string) bool { return validCommandActions[a] }

// CommandResult is the agent's reply to a Command. Status is one of:
//   - "ok"      — action completed without error
//   - "err"     — action failed; Output carries the diagnostic
//   - "locked"  — another instance of the same action holds the lock-file
//   - "timeout" — action exceeded its per-action timeout
//
// Payload carries action-specific structured data when the human-readable
// Output isn't enough. Currently used by opkg_upgrade / opkg_feed_disable to
// surface `failed_feeds` to the backend so it can render repair buttons.
// Optional: omitted entirely when the action has no structured response,
// preserving wire compatibility with old agents/backends.
type CommandResult struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Output     string          `json:"output,omitempty"`
	DurationMs int64           `json:"duration_ms"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

var validCommandResultStatuses = map[string]bool{
	"ok": true, "err": true, "locked": true, "timeout": true,
}

func IsValidCommandResultStatus(s string) bool { return validCommandResultStatuses[s] }

// OpkgUpgradeResult is the structured payload returned by opkg_upgrade and
// opkg_feed_disable. Output mirrors the human-readable text in
// CommandResult.Output (the backend stays canonical for rendering).
// FailedFeeds is the list of URLs opkg failed to download during the update
// step; non-empty triggers the repair-button UI on the backend.
type OpkgUpgradeResult struct {
	Output      string   `json:"output,omitempty"`
	FailedFeeds []string `json:"failed_feeds,omitempty"`
}

// IsZero reports whether the payload carries no actionable data. Used by the
// agent runner to decide whether to attach the payload to CommandResult.
func (r OpkgUpgradeResult) IsZero() bool {
	return r.Output == "" && len(r.FailedFeeds) == 0
}
