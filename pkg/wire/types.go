// Package wire defines the JSON wire format shared by agent and backend.
// Field tags here are the contract — changing them is a breaking change.
package wire

import "time"

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
