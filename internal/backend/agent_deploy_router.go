package backend

// awgmInstallJob drives the relay's bootstrap_install mode: a first-time agent
// install over the AWG Manager terminal. Credentials are transient (0600 temp
// file, deleted after the run) — nothing here is persisted.
//
// This struct is the JSON shape runProvisionInstallCore (provision_handler.go)
// assembles for both dashboardHandleProvisionInstall and
// dashboardHandleRepairReinstall before handing the marshalled job to the
// provisioning engine (provision.Deps.Start / provision.DefaultRelay,
// internal/backend/provision/relay.go). The route this file used to serve on
// its own — POST .../deploy-router, and the synchronous runAWGMInstallJob
// runner that drove it directly — was removed once the dashboard switched to
// that async engine (Task 14); only the job shape survived, reused as-is.
type awgmInstallJob struct {
	BaseURL          string            `json:"base_url"`
	APIKey           string            `json:"api_key,omitempty"`
	Login            string            `json:"login,omitempty"`
	Password         string            `json:"password,omitempty"`
	TerminalUser     string            `json:"terminal_user"`
	TerminalPassword string            `json:"terminal_password"`
	Mode             string            `json:"mode"`
	Nickname         string            `json:"nickname"`
	TargetVersion    string            `json:"target_version"`
	BackendURL       string            `json:"backend_url"`
	RawToken         string            `json:"raw_token"`
	ReleaseBase      string            `json:"release_base"`
	InitScript       string            `json:"init_script"`
	Checksums        map[string]string `json:"checksums"`
}
