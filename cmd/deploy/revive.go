package main

import (
	"fmt"
	"strings"
)

// Revive flow. A router goes dark when its backend domain changes: the agent
// keeps dialing the OLD backend.url in /opt/etc/wg-monitor/config.yaml, so it
// never polls the current backend and no command (dashboard or wizard) can
// reach it. The only channel that still works is SSH — which the wizard has.
//
// "Revive" = the normal update-agent SSH flow PLUS a config-refresh step that
// rewrites backend.url to the wizard's current backend URL (preserving the
// token and every other setting), then pushes the current agent binary and
// restarts. It is the operator-side "redeploy with the change applied".

// replaceAgentConfigBackendURL rewrites the `url:` line inside the `backend:`
// section of an agent config.yaml to newURL, preserving comments, the token and
// every other section. Mirrors the agent-side substituteBackendURL but also
// reports whether anything actually changed (so revive can skip a needless
// write when the config is already correct).
func replaceAgentConfigBackendURL(content, newURL string) (string, bool, error) {
	lines := strings.Split(content, "\n")
	inBackend := false
	found := false
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Top-level section change: an unindented, non-comment `name:` line.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			inBackend = strings.HasSuffix(trimmed, ":") && strings.TrimSuffix(trimmed, ":") == "backend"
		}
		if inBackend && !found {
			stripped := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(stripped, "url:") {
				indent := line[:len(line)-len(stripped)]
				newLine := indent + "url: " + newURL
				found = true
				if newLine != line {
					lines[i] = newLine
					changed = true
				}
			}
		}
	}
	if !found {
		return "", false, fmt.Errorf("backend.url line not found in config")
	}
	return strings.Join(lines, "\n"), changed, nil
}

// refreshAgentBackendURLOverSSH backs up and rewrites the router's config.yaml
// backend.url to the wizard's current backend URL. No-op (beyond a log line)
// when it already matches. Must run on an SSH session that is already connected
// and past the install/MAC-pin guards.
func refreshAgentBackendURLOverSSH(s *SSH, state *State, ag *AgentState) error {
	want := backendURLForAgent(state)
	if want == "" {
		return fmt.Errorf("revive: в wizard.toml нет backend домена — сначала install-backend")
	}
	current := stepReadOrEmpty(s, "cat /opt/etc/wg-monitor/config.yaml 2>/dev/null")
	if strings.TrimSpace(current) == "" {
		return fmt.Errorf("revive: на %s нет /opt/etc/wg-monitor/config.yaml — агент не установлен, запусти [3] install/add-router", ag.Nickname)
	}
	updated, changed, err := replaceAgentConfigBackendURL(current, want)
	if err != nil {
		return fmt.Errorf("revive: %w", err)
	}
	if !changed {
		PrintOK("backend.url уже " + want + " — обновлю бинарь и рестартну")
		return nil
	}
	// Backup before overwrite (mirrors add-router's migrate backup).
	s.Run("cp -p /opt/etc/wg-monitor/config.yaml /opt/etc/wg-monitor/config.yaml.bak-revive-$(date +%Y%m%d%H%M%S) 2>/dev/null || true")
	if err := s.UploadStdin("/opt/etc/wg-monitor/config.yaml", []byte(updated)); err != nil {
		return fmt.Errorf("revive: upload config: %w", err)
	}
	if _, err := s.MustRun("chmod 600 /opt/etc/wg-monitor/config.yaml"); err != nil {
		return err
	}
	PrintOK("backend.url → " + want)
	return nil
}
