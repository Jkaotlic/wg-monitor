package main

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

// BackendParams drives backend.yaml.tmpl. The bot token is uploaded as a
// separate /etc/wg-monitor/bot-token.txt file (referenced by bot_token_file
// in the rendered yaml) — it is NOT a template variable. Agents/users live
// in the SQLite DB and are added via `wg-monitor-cli add-user` on the VPS,
// not via this template.
type BackendParams struct {
	ChatID      int64
	AdminUserID int64
}

type AgentParams struct {
	BackendURL string
	Token      string
	Nickname   string
}

type CaddyParams struct {
	Domain string
	Email  string
}

type BackupServiceParams struct {
	User            string
	Group           string
	BinaryPath      string
	ConfigPath      string
	PassphrasePath  string
	OperatorVault   string
	OutDir          string
	LayoutRoot      string
	ReadWritePath   string
	SendTelegram    bool
	ProtectHomeMode string
}

func renderTemplate(name string, data any) ([]byte, error) {
	raw, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded template %s: %w", name, err)
	}
	t, err := template.New(name).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func RenderBackendYAML(p BackendParams) ([]byte, error) {
	return renderTemplate("backend.yaml.tmpl", p)
}

func RenderAgentYAML(p AgentParams) ([]byte, error) {
	return renderTemplate("agent.yaml.tmpl", p)
}

func RenderCaddyfile(p CaddyParams) ([]byte, error) {
	return renderTemplate("Caddyfile.tmpl", p)
}

func RenderBackupService(p BackupServiceParams) ([]byte, error) {
	if p.User == "" {
		p.User = "wgmonitor"
	}
	if p.Group == "" {
		p.Group = p.User
	}
	if p.BinaryPath == "" {
		p.BinaryPath = "/usr/local/bin/wg-monitor-backend"
	}
	if p.ConfigPath == "" {
		p.ConfigPath = "/etc/wg-monitor/backend.yaml"
	}
	if p.PassphrasePath == "" {
		p.PassphrasePath = "/etc/wg-monitor/backup-passphrase.txt"
	}
	if p.OperatorVault == "" {
		p.OperatorVault = "/var/lib/wg-monitor/operator-secrets.tgz.enc"
	}
	if p.OutDir == "" {
		p.OutDir = "/var/lib/wg-monitor/backups"
	}
	if p.ReadWritePath == "" {
		p.ReadWritePath = "/var/lib/wg-monitor"
	}
	if p.ProtectHomeMode == "" {
		p.ProtectHomeMode = "true"
	}
	return renderTemplate("wg-monitor-backup.service.tmpl", p)
}

// ReadStaticTemplate returns an embedded file verbatim (no template processing).
// Use for files like S99wg-monitor and wg-monitor-backend.service.
func ReadStaticTemplate(name string) ([]byte, error) {
	return templatesFS.ReadFile("templates/" + name)
}
