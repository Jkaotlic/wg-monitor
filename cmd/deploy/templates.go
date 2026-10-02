package main

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strings"
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
	PublicBaseURL string
	AdminUserID   int64
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
	User           string
	Group          string
	BinaryPath     string
	ConfigPath     string
	PassphrasePath string
	OperatorVault  string
	OutDir         string
	LayoutRoot     string
	ReadWritePath  string
	SendTelegram   bool
	// OffsiteSCP -- куда копировать полный архив (user@host:path); пусто --
	// внешняя цель не настроена. OffsiteKey -- файл ключа SSH на бэкенде.
	OffsiteSCP      string
	OffsiteKey      string
	ProtectHomeMode string
	OmitUserGroup   bool
	OmitHardening   bool
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

// Значения уходят в строку ExecStart без кавычек: пробел или перевод строки
// в них стал бы новым аргументом или новой директивой юнита.
var (
	backupOffsiteTargetRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._\[\]:-]+:[A-Za-z0-9._~/+-]+$`)
	backupOffsiteKeyRe    = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+$`)
)

func RenderBackupService(p BackupServiceParams) ([]byte, error) {
	p.OffsiteSCP, p.OffsiteKey = strings.TrimSpace(p.OffsiteSCP), strings.TrimSpace(p.OffsiteKey)
	if p.OffsiteSCP != "" || p.OffsiteKey != "" {
		if !backupOffsiteTargetRe.MatchString(p.OffsiteSCP) {
			return nil, fmt.Errorf("backup offsite target must look like user@host:/path without spaces, got %q", p.OffsiteSCP)
		}
		if !backupOffsiteKeyRe.MatchString(p.OffsiteKey) {
			return nil, fmt.Errorf("backup offsite key must be an absolute path without spaces, got %q", p.OffsiteKey)
		}
	}
	return renderTemplate("wg-monitor-backup.service.tmpl", backupServiceDefaults(p))
}

// RenderBackupVerifyService -- служба еженедельной проверки восстановления.
func RenderBackupVerifyService(p BackupServiceParams) ([]byte, error) {
	return renderTemplate("wg-monitor-backup-verify.service.tmpl", backupServiceDefaults(p))
}

func backupServiceDefaults(p BackupServiceParams) BackupServiceParams {
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
	return p
}

// ReadStaticTemplate returns an embedded file verbatim (no template processing).
// Use for files like S99wg-monitor and wg-monitor-backend.service.
func ReadStaticTemplate(name string) ([]byte, error) {
	return templatesFS.ReadFile("templates/" + name)
}
