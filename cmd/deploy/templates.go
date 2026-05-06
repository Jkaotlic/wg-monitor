package main

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

type AgentEntry struct {
	Nickname string
	Token    string
	ThreadID int
}

type BackendParams struct {
	BotToken    string
	ChatID      int64
	AdminUserID int64
	Agents      []AgentEntry
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
