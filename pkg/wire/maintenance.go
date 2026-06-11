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

// OpkgCronStatus is returned by opkg_cron_* commands. It is intentionally
// router-truth shaped: the dashboard renders these fields directly after a
// fresh command result instead of persisting stale install state in backend DB.
type OpkgCronStatus struct {
	Installed   bool   `json:"installed"`
	Schedule    string `json:"schedule,omitempty"`
	ScriptPath  string `json:"script_path"`
	CronPath    string `json:"cron_path,omitempty"`
	LogPath     string `json:"log_path"`
	CronService string `json:"cron_service,omitempty"`
	FreeKB      int64  `json:"free_kb,omitempty"`
	TotalKB     int64  `json:"total_kb,omitempty"`
	MinFreeKB   int64  `json:"min_free_kb,omitempty"`
	LastRun     string `json:"last_run,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`
	LogTail     string `json:"log_tail,omitempty"`
}

// EntwareCleanStatus is returned by entware_clean_* commands. It mirrors live
// router state after the managed cleanup script is installed, run, queried, or
// removed.
type EntwareCleanStatus struct {
	Installed         bool   `json:"installed"`
	Schedule          string `json:"schedule,omitempty"`
	ScriptPath        string `json:"script_path"`
	CronPath          string `json:"cron_path,omitempty"`
	LogPath           string `json:"log_path"`
	CronService       string `json:"cron_service,omitempty"`
	FreeKB            int64  `json:"free_kb,omitempty"`
	TotalKB           int64  `json:"total_kb,omitempty"`
	MinFreeKB         int64  `json:"min_free_kb,omitempty"`
	MemAvailableKB    int64  `json:"mem_available_kb,omitempty"`
	MemTotalKB        int64  `json:"mem_total_kb,omitempty"`
	MinMemAvailableKB int64  `json:"min_mem_available_kb,omitempty"`
	LastRun           string `json:"last_run,omitempty"`
	LastStatus        string `json:"last_status,omitempty"`
	LastFreedKB       int64  `json:"last_freed_kb,omitempty"`
	LogTail           string `json:"log_tail,omitempty"`
}
