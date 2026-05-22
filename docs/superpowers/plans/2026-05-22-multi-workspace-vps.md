# Multi-Workspace VPS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one VPS host multiple isolated wg-monitor customer workspaces, each with its own Telegram bot, forum group, backend URL, database, wizard token, and router fleet.

**Architecture:** Start with multi-instance isolation, not in-process multi-tenancy. Each workspace runs a separate backend service on a unique localhost port with a separate config, DB, token files, Caddy route, wizard profile, cache, secrets, and backup path. The existing `--profile` support becomes the operator-facing workspace selector.

**Tech Stack:** Go deploy wizard, systemd, Caddy, SQLite, Telegram Bot API, existing backend/wizard endpoints, GitHub release assets.

---

## Current Constraints

- Backend config currently has one `telegram.bot_token_file`, one `telegram.chat_id`, and one `telegram.admin_user_id`.
- Runtime uses one `tg.Client`, one callback router, and one alert dispatcher per backend process.
- Router-topic binding is stored as `users.telegram_thread_id`; it assumes all topics live in the configured `chat_id`.
- Wizard already has `--profile`, isolated `wizard.toml`, isolated cache, isolated lock, and isolated secrets path support. This is the right foundation for customer workspaces.

## Target Model

Workspace `default` keeps the current paths:

- Backend service: `wg-monitor-backend.service`
- Backend config: `/etc/wg-monitor/backend.yaml`
- Backend DB: `/var/lib/wg-monitor/state.db`
- Backend listen: `127.0.0.1:8080`
- Wizard profile: default

Workspace `client-a` gets isolated paths:

- Backend service: `wg-monitor-backend@client-a.service`
- Backend config: `/etc/wg-monitor/workspaces/client-a/backend.yaml`
- Backend DB: `/var/lib/wg-monitor/workspaces/client-a/state.db`
- Backend listen: `127.0.0.1:8081`
- Bot token: `/etc/wg-monitor/workspaces/client-a/bot-token.txt`
- Wizard token: `/etc/wg-monitor/workspaces/client-a/wizard-token.txt`
- Caddy route: `client-a.example.com -> 127.0.0.1:8081`
- Wizard profile: `--profile client-a`

## Files

- Modify: `cmd/deploy/menu.go` to expose a Workspaces section.
- Modify: `cmd/deploy/main.go` to show the active profile/workspace in CLI and header output.
- Modify: `cmd/deploy/state.go` only if a workspace registry path helper is needed.
- Create: `cmd/deploy/workspaces.go` for registry, port allocation, service naming, and workspace selection.
- Create: `cmd/deploy/workspaces_test.go` for path, port, and profile isolation tests.
- Modify: `cmd/deploy/actions.go` to make backend install/update workspace-aware.
- Modify: `cmd/deploy/templates.go` to render workspace-specific backend config and service units.
- Modify: `cmd/deploy/restore_backup.go` so restore asks which workspace to restore into.
- Modify: `cmd/deploy/backup.go` or the current backup implementation so backup filenames include the workspace.
- Modify: `DEPLOY.md` and `README.md` to document one-VPS/many-workspaces operations.

## Task 1: Workspace Registry

**Files:**
- Create: `cmd/deploy/workspaces.go`
- Create: `cmd/deploy/workspaces_test.go`
- Modify: `cmd/deploy/state.go`

- [ ] **Step 1: Write failing tests for workspace sanitization and paths**

```go
func TestWorkspaceNameSanitizesLikeProfile(t *testing.T) {
	got := sanitizeWorkspaceName(" Client A! ")
	if got != "clienta" {
		t.Fatalf("workspace=%q, want clienta", got)
	}
}

func TestWorkspacePathsDefaultAndNamed(t *testing.T) {
	def := WorkspacePaths("")
	if def.ServiceName != "wg-monitor-backend" {
		t.Fatalf("default service=%q", def.ServiceName)
	}
	named := WorkspacePaths("client-a")
	if named.ServiceName != "wg-monitor-backend@client-a" {
		t.Fatalf("named service=%q", named.ServiceName)
	}
	if named.ConfigPath != "/etc/wg-monitor/workspaces/client-a/backend.yaml" {
		t.Fatalf("config path=%q", named.ConfigPath)
	}
	if named.DBPath != "/var/lib/wg-monitor/workspaces/client-a/state.db" {
		t.Fatalf("db path=%q", named.DBPath)
	}
}
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test ./cmd/deploy -run TestWorkspace -count=1`

Expected: build fails because `sanitizeWorkspaceName` and `WorkspacePaths` do not exist.

- [ ] **Step 3: Add workspace path model**

```go
type WorkspaceRuntimePaths struct {
	Name            string
	ServiceName     string
	ConfigPath      string
	DBPath          string
	BotTokenPath    string
	WizardTokenPath string
	ListenAddr      string
}

func sanitizeWorkspaceName(name string) string {
	return sanitizeProfileName(strings.TrimSpace(name))
}

func WorkspacePaths(name string) WorkspaceRuntimePaths {
	name = sanitizeWorkspaceName(name)
	if name == "" || name == "default" {
		return WorkspaceRuntimePaths{
			Name:            "default",
			ServiceName:     "wg-monitor-backend",
			ConfigPath:      "/etc/wg-monitor/backend.yaml",
			DBPath:          "/var/lib/wg-monitor/state.db",
			BotTokenPath:    "/etc/wg-monitor/bot-token.txt",
			WizardTokenPath: "/etc/wg-monitor/wizard-token.txt",
			ListenAddr:      "127.0.0.1:8080",
		}
	}
	baseEtc := "/etc/wg-monitor/workspaces/" + name
	baseVar := "/var/lib/wg-monitor/workspaces/" + name
	return WorkspaceRuntimePaths{
		Name:            name,
		ServiceName:     "wg-monitor-backend@" + name,
		ConfigPath:      baseEtc + "/backend.yaml",
		DBPath:          baseVar + "/state.db",
		BotTokenPath:    baseEtc + "/bot-token.txt",
		WizardTokenPath: baseEtc + "/wizard-token.txt",
	}
}
```

- [ ] **Step 4: Add deterministic port allocation**

Add a registry file under the wizard cache, for example `workspaces.toml`, with rows `{name, domain, port}`. The default workspace keeps port `8080`; named workspaces allocate from `8081` upward and never reuse a port still present in the registry.

- [ ] **Step 5: Verify tests pass**

Run: `go test ./cmd/deploy -run TestWorkspace -count=1`

Expected: tests pass.

## Task 2: Workspace-Aware Backend Install

**Files:**
- Modify: `cmd/deploy/actions.go`
- Modify: `cmd/deploy/templates.go`
- Test: `cmd/deploy/templates_test.go`

- [ ] **Step 1: Add template tests**

Add a test that renders a named workspace backend config and asserts:

- `listen: 127.0.0.1:8081`
- `db_path: /var/lib/wg-monitor/workspaces/client-a/state.db`
- `bot_token_file: /etc/wg-monitor/workspaces/client-a/bot-token.txt`
- `wizard.token_file: /etc/wg-monitor/workspaces/client-a/wizard-token.txt`

- [ ] **Step 2: Run template test and confirm it fails**

Run: `go test ./cmd/deploy -run TestRenderBackendYAML -count=1`

Expected: failure until templates accept workspace paths.

- [ ] **Step 3: Thread `WorkspaceRuntimePaths` into backend install**

Backend install must write workspace-specific files, create both `/etc` and `/var/lib` workspace directories, and start the correct service name.

- [ ] **Step 4: Keep default workspace backward-compatible**

Existing default install must still write `/etc/wg-monitor/backend.yaml`, `/etc/wg-monitor/bot-token.txt`, `/etc/wg-monitor/wizard-token.txt`, and `/var/lib/wg-monitor/state.db`.

- [ ] **Step 5: Verify**

Run: `go test ./cmd/deploy -run "TestRenderBackendYAML|TestWorkspace" -count=1`

Expected: tests pass.

## Task 3: Caddy Multi-Route Support

**Files:**
- Modify: `cmd/deploy/actions.go`
- Create or modify tests around Caddy rendering if they already exist.

- [ ] **Step 1: Write tests for Caddy route rendering**

Assert that a named workspace with domain `client-a.example.com` and port `8081` creates a route equivalent to:

```caddyfile
client-a.example.com {
	reverse_proxy 127.0.0.1:8081
}
```

- [ ] **Step 2: Implement additive Caddy updates**

Do not overwrite unrelated workspace routes. Either manage a clearly marked block per workspace or write one imported file per workspace under `/etc/caddy/wg-monitor.d/<workspace>.caddy`.

- [ ] **Step 3: Reload and verify**

Remote command should run `caddy validate --config /etc/caddy/Caddyfile` before `systemctl reload caddy`.

## Task 4: Wizard Workspace Menu

**Files:**
- Modify: `cmd/deploy/menu.go`
- Modify: `cmd/deploy/main.go`
- Create tests in `cmd/deploy/menu_test.go` if needed.

- [ ] **Step 1: Add menu entry**

Add a top-level entry: `Workspaces`.

- [ ] **Step 2: Add actions**

The Workspaces menu should support:

- list workspaces
- create workspace
- switch active workspace
- show workspace status

- [ ] **Step 3: Profile integration**

Switching workspace should set the same active value used by `--profile`, so cache, lock, state, and secrets stay isolated.

- [ ] **Step 4: Verify**

Run: `go test ./cmd/deploy -run "TestMenu|TestWorkspace" -count=1`

Expected: tests pass.

## Task 5: Backup And Restore Isolation

**Files:**
- Modify: backup implementation
- Modify: `cmd/deploy/restore_backup.go`
- Add tests next to existing backup/restore tests.

- [ ] **Step 1: Add backup manifest workspace field**

Backup manifest should include:

```text
workspace=client-a
domain=client-a.example.com
service=wg-monitor-backend@client-a
db_path=/var/lib/wg-monitor/workspaces/client-a/state.db
```

- [ ] **Step 2: Restore into a selected workspace**

Restore flow must ask whether to restore as default workspace or a named workspace. Named restore must not overwrite default `/etc/wg-monitor/backend.yaml` or `/var/lib/wg-monitor/state.db`.

- [ ] **Step 3: Verify**

Run: `go test ./cmd/deploy -run "Backup|Restore|Workspace" -count=1`

Expected: tests pass.

## Task 6: Operational Limits And Guardrails

**Files:**
- Modify: `cmd/deploy/doctor.go`
- Modify: `DEPLOY.md`

- [ ] **Step 1: Add VPS capacity check**

Doctor should show per-workspace services, ports, DB sizes, and process RSS. Warn when available memory is below 200 MiB or disk free is below 1 GiB.

- [ ] **Step 2: Add Telegram isolation check**

For each workspace, verify the bot can access its configured `chat_id` and has topic-management permissions.

- [ ] **Step 3: Document scaling**

Document that the current small VPS is suitable for several low-volume workspaces, while dozens of active workspaces should move to a larger VPS or true in-process multi-tenancy.

## Task 7: Release

**Files:**
- Modify: `README.md`
- Add: `docs/releases/v0.13.0-rcXX.md`

- [ ] **Step 1: Run full verification**

Run: `go test ./... -count=1`

Expected: all packages pass.

- [ ] **Step 2: Commit**

Commit message: `feat(deploy): plan multi-workspace VPS support`

- [ ] **Step 3: Tag next RC**

Tag: next `v0.13.0-rcN` after the latest published RC.

- [ ] **Step 4: Push branch and tag**

Run: `git push origin main` and `git push origin v0.13.0-rcN`.

- [ ] **Step 5: Verify GitHub release**

Use GitHub Actions and release checks to ensure the tag built all deploy/backend/CLI/agent assets and the release is marked prerelease.

## Self-Review

- Spec coverage: covers second bot/group/client deployment, current one-workspace limitation, one-VPS multi-instance support, restore/backup implications, and resource guardrails.
- Placeholder scan: no TBD/TODO placeholders.
- Type consistency: workspace terminology maps to existing deploy `--profile` behavior and keeps backend runtime isolated per service.
