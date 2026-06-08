package main

import (
	"strings"
	"testing"
)

func TestMainMenuIsTaskOrientedAndHasHelp(t *testing.T) {
	items := mainMenuItems(false, &State{})
	if len(items) != 9 {
		t.Fatalf("len(mainMenuItems)=%d, want 9: %+v", len(items), items)
	}
	text := renderMenuItems(items, "Q", "Выход")
	for _, want := range []string{
		"[1] VPS / backend",
		"[2] Проверить / обновить",
		"[3] Роутеры",
		"[4] Переезд на новый VPS",
		"[5] Doctor",
		"[6] Синхронизация с VPS",
		"[7] Backups",
		"[8] Restore / Disaster Recovery",
		"[9] Сервис",
		"когда нажимать:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("main menu missing %q:\n%s", want, text)
		}
	}
	for _, oldTopLevel := range []string{
		"Re-enroll через AWG Manager",
		"Открыть wizard.toml",
		"Забыть known_hosts",
		"Удалить агента",
	} {
		if strings.Contains(text, oldTopLevel) {
			t.Fatalf("old top-level action %q leaked into main menu:\n%s", oldTopLevel, text)
		}
	}
}

func TestBackupMenuGroupsBackupActions(t *testing.T) {
	text := renderMenuItems(backupMenuItems(), "B", "back")
	for _, want := range []string{
		"[1] Status",
		"[2] Enable / reinstall nightly",
		"[3] Run backup now",
		"[4] Push wizard secrets",
		"[5] Show / rotate password",
		"[6] Restore encrypted backup",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("backup menu missing %q:\n%s", want, text)
		}
	}
}

func TestRouterMenuGroupsRouterActions(t *testing.T) {
	text := renderMenuItems(routerMenuItems(), "B", "Назад")
	for _, want := range []string{
		"[1] Добавить новый",
		"[2] Re-enroll / переустановить",
		"[3] Удалить агента",
		"[4] Показать роутеры",
		"[5] Telegram group/topic",
		"AWG Manager/KeenDNS",
		"новый token",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("router menu missing %q:\n%s", want, text)
		}
	}
}

func TestServiceMenuHidesLegacyUnlessEnabled(t *testing.T) {
	plain := renderMenuItems(serviceMenuItems(false), "B", "Назад")
	if strings.Contains(plain, "Legacy Netfix") {
		t.Fatalf("legacy item visible without flag:\n%s", plain)
	}
	legacy := renderMenuItems(serviceMenuItems(true), "B", "Назад")
	if !strings.Contains(legacy, "Legacy Netfix") {
		t.Fatalf("legacy item missing with flag:\n%s", legacy)
	}
}

func TestServiceMenuOffersCurrentVPSAdoption(t *testing.T) {
	text := renderMenuItems(serviceMenuItems(false), "B", "back")
	if !strings.Contains(text, "current VPS") && !strings.Contains(text, "текущий VPS") {
		t.Fatalf("service menu should offer current VPS adoption:\n%s", text)
	}
}

func TestMenuHeaderBoxKeepsVersionInsideBorder(t *testing.T) {
	line := boxSplit("WG MONITOR // Deploy Console", "v0.13.0-rc-super-long")
	if got := len([]rune(line)); got != menuBoxInnerWidth+2 {
		t.Fatalf("boxSplit width=%d, want %d: %q", got, menuBoxInnerWidth+2, line)
	}
	if !strings.HasPrefix(line, "║") || !strings.HasSuffix(line, "║") {
		t.Fatalf("boxSplit lost borders: %q", line)
	}
	if !strings.Contains(line, "v0.13.0-rc-super-long") {
		t.Fatalf("boxSplit lost version: %q", line)
	}
}

func TestMenuHeaderBoxTruncatesLongRows(t *testing.T) {
	line := boxLine("VPS    83.171.224.125  very-long-domain-name-that-would-otherwise-break-the-box.example")
	if got := len([]rune(line)); got != menuBoxInnerWidth+2 {
		t.Fatalf("boxLine width=%d, want %d: %q", got, menuBoxInnerWidth+2, line)
	}
	if !strings.Contains(line, "...") {
		t.Fatalf("boxLine did not truncate long content: %q", line)
	}
}

func TestStatusRowsKeepDeployVersionAndTime(t *testing.T) {
	state := &State{
		Backend: BackendState{
			Host: "83.171.224.125", Domain: "wgmonitor.example", LastDeployedVersion: "v0.13.0-rc12", LastDeploy: "2026-05-21T12:34:56Z",
		},
		Agents: []AgentState{{
			Nickname: "alyaba", Host: "192.168.31.1", Port: 22, LastDeployedVersion: "v0.13.0-rc12", LastDeploy: "2026-05-21T12:40:00Z",
		}},
	}

	rows := menuStatusRows(state)
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"VPS    83.171.224.125  wgmonitor.example",
		"installed v0.13.0-rc12 at 2026-05-21 12:34",
		"v0.13.0-rc12",
		"Router 192.168.31.1:22  alyaba",
		"installed v0.13.0-rc12 at 2026-05-21 12:40",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("status rows missing %q:\n%s", want, joined)
		}
	}
	if got := len(rows); got != 4 {
		t.Fatalf("len(status rows)=%d, want 4: %#v", got, rows)
	}
}

func TestStatusRowsUseReadableEndpointForAWGMOnlyRouters(t *testing.T) {
	state := &State{Agents: []AgentState{{
		Nickname:            "bronya",
		DeployMode:          "awgm",
		AWGMURL:             "https://awg.bronya.example.test",
		LastDeployedVersion: "v0.13.0-rc60",
	}}}
	rows := strings.Join(menuStatusRows(state), "\n")
	if !strings.Contains(rows, "Router AWGM  bronya") {
		t.Fatalf("AWGM-only status row should not render -:0:\n%s", rows)
	}
	if strings.Contains(rows, "-:0") {
		t.Fatalf("status rows leaked empty SSH endpoint:\n%s", rows)
	}
}

func TestStatusRowsUseReadableEndpointForMetadataGapRouters(t *testing.T) {
	state := &State{Agents: []AgentState{{
		Nickname:            "bronya",
		LastDeployedVersion: "v0.13.0-rc60",
	}}}
	rows := strings.Join(menuStatusRows(state), "\n")
	if !strings.Contains(rows, "Router metadata-gap  bronya") {
		t.Fatalf("metadata-gap status row should name the state instead of -:0:\n%s", rows)
	}
}

func TestAgentDeployModeLabelShowsMetadataGapInsteadOfLegacy(t *testing.T) {
	ag := AgentState{
		Nickname:            "bronya",
		LastDeployedVersion: "v0.13.0-rc59",
	}
	if got := agentDeployModeLabel(ag); got != "metadata-gap" {
		t.Fatalf("mode label=%q, want metadata-gap", got)
	}
	ag.Host = "192.168.31.1"
	if got := agentDeployModeLabel(ag); got != "legacy-ssh" {
		t.Fatalf("agent with SSH coords should stay legacy-ssh, got %q", got)
	}
	ag.Host = ""
	ag.DeployMode = "awgm"
	if got := agentDeployModeLabel(ag); got != "awgm" {
		t.Fatalf("explicit mode should win, got %q", got)
	}
}

func TestAgentSSHEndpointLabelHidesEmptyPort(t *testing.T) {
	if got := agentSSHEndpointLabel(AgentState{}); got != "-" {
		t.Fatalf("empty SSH endpoint=%q, want -", got)
	}
	if got := agentSSHEndpointLabel(AgentState{Host: "192.168.31.1", Port: 222}); got != "192.168.31.1:222" {
		t.Fatalf("SSH endpoint=%q", got)
	}
}

func TestAgentChoiceLabelUsesReadableEndpoint(t *testing.T) {
	cases := []struct {
		name string
		ag   AgentState
		want string
	}{
		{
			name: "metadata gap",
			ag: AgentState{
				Nickname:            "bronya",
				LastDeployedVersion: "v0.13.0-rc60",
			},
			want: "bronya (metadata-gap)",
		},
		{
			name: "awgm only",
			ag: AgentState{
				Nickname:   "alyaba",
				DeployMode: "awgm",
				AWGMURL:    "https://awg.alyaba.example.test",
			},
			want: "alyaba (AWGM)",
		},
		{
			name: "ssh",
			ag:   AgentState{Nickname: "testkeen", Host: "192.168.31.1", Port: 222},
			want: "testkeen (192.168.31.1:222)",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentChoiceLabel(tt.ag); got != tt.want {
				t.Fatalf("agentChoiceLabel=%q, want %q", got, tt.want)
			}
			if strings.Contains(agentChoiceLabel(tt.ag), ":0") {
				t.Fatalf("choice label leaked :0: %q", agentChoiceLabel(tt.ag))
			}
		})
	}
}

func TestKnownRouterLineUsesReadableAWGMAndSSHLabels(t *testing.T) {
	line := knownRouterLine(1, AgentState{
		Nickname:            "bronya",
		LastDeployedVersion: "v0.13.0-rc61",
	})
	for _, want := range []string{"[1] bronya", "mode=metadata-gap", "awgm=-", "ssh=-"} {
		if !strings.Contains(line, want) {
			t.Fatalf("known router line missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "-:0") {
		t.Fatalf("known router line leaked unreadable empty endpoint:\n%s", line)
	}
}

func TestKnownRoutersMetadataGapHintExplainsSafeNextStep(t *testing.T) {
	state := &State{Agents: []AgentState{{
		Nickname:            "bronya",
		LastDeployedVersion: "v0.13.0-rc59",
	}}}
	got := knownRoutersMetadataGapHint(state)
	for _, want := range []string{
		"metadata-gap",
		"bronya",
		"sync-vps",
		"re-enroll",
		"token",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metadata-gap hint missing %q:\n%s", want, got)
		}
	}
	state.Agents[0].AWGMURL = "https://awg.bronya.example.test"
	if got := knownRoutersMetadataGapHint(state); got != "" {
		t.Fatalf("complete AWGM metadata should not warn, got %q", got)
	}
}

func TestBoxLinesUseTerminalCellWidth(t *testing.T) {
	line := boxLine("Router 192.168.31.1:222  тестроутер | v0.13.0-rc12 | 2026-05-21 12:40")
	if got := terminalCells(line); got != menuBoxInnerWidth+2 {
		t.Fatalf("terminal width=%d, want %d: %q", got, menuBoxInnerWidth+2, line)
	}
	if !strings.HasPrefix(line, "║") || !strings.HasSuffix(line, "║") {
		t.Fatalf("boxLine lost borders: %q", line)
	}
}

func TestBoxLinePreservesStatusIndent(t *testing.T) {
	line := boxLine("       installed v0.13.0-rc12 at 2026-05-21 12:40")
	if !strings.Contains(line, "║        installed") {
		t.Fatalf("boxLine stripped status indent: %q", line)
	}
}

func TestInstalledStatusRowsAreDetectedForHighlight(t *testing.T) {
	if !isInstalledStatusRow("       installed v0.13.0-rc12 at 2026-05-21 12:40") {
		t.Fatal("installed status row should be highlighted")
	}
	if isInstalledStatusRow("Router 192.168.31.1:222  testkeen") {
		t.Fatal("router identity row should not be highlighted")
	}
}

func TestShouldWarnStartupSyncOnlyForAuthFailures(t *testing.T) {
	if !shouldWarnStartupSync(errString("GET /v1/wizard/agents: HTTP 401")) {
		t.Fatal("expected auth failure to warn")
	}
	if shouldWarnStartupSync(errString("context deadline exceeded")) {
		t.Fatal("startup timeout should silently fall back to local cache")
	}
	if shouldWarnStartupSync(errString("lookup wgmonitor.example: no such host")) {
		t.Fatal("startup DNS flap should silently fall back to local cache")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
