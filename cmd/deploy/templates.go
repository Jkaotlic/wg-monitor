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
	BackendURL     string
	Token          string
	Nickname       string
	AWGIface       string
	ExpectedExitIP string
}

type CaddyParams struct {
	Domain string
	Email  string
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

// ReadStaticTemplate returns an embedded file verbatim (no template processing).
// Use for files like S99wg-monitor and wg-monitor-backend.service.
func ReadStaticTemplate(name string) ([]byte, error) {
	return templatesFS.ReadFile("templates/" + name)
}
