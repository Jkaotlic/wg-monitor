// Package wire — maintenance.go defines payload types for version_audit and
// firmware_status commands. They are JSON-encoded into wire.CommandResult.Output;
// no wire envelope additions are required besides the action names.
package wire

// VersionAudit is the agent's compact reply to a version_audit command.
// Backend uses it to render the Maintenance panel and to compute soft-warning
// updates for the smart-reply.
type VersionAudit struct {
	AwgmgrVersion   string `json:"awgmgr_version"`
	AwgmgrBackend   string `json:"awgmgr_backend,omitempty"`
	AwgmgrRunning   bool   `json:"awgmgr_running,omitempty"`
	HrneoInstalled  bool   `json:"hrneo_installed,omitempty"`
	HrneoRunning    bool   `json:"hrneo_running,omitempty"`
	HrneoVersion    string `json:"hrneo_version,omitempty"`
	FirmwareCurrent string `json:"firmware_current"`
	FirmwareAvail   string `json:"firmware_avail,omitempty"`
	HrneoUptime     string `json:"hrneo_uptime,omitempty"`
	AwgmgrUptime    string `json:"awgmgr_uptime,omitempty"`
}

// FirmwareStatus is the agent's reply to a firmware_status command.
// Mirrors the relevant fields from `ndmc -c "components list"` (firmware
// vs local sub-blocks).
type FirmwareStatus struct {
	Current   string `json:"current"`
	Available string `json:"available,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Channel   string `json:"channel,omitempty"`
}
