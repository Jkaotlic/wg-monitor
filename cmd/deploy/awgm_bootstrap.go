package main

import (
	"fmt"
	"strings"
)

type AWGMBootstrapParams struct {
	Nickname     string
	BackendURL   string
	RawToken     string
	Version      string
	DownloadURL  string
	ChecksumURL  string
	ChecksumName string
}

type awgmBootstrapTemplateParams struct {
	NicknameQ     string
	VersionQ      string
	DownloadURLQ  string
	ChecksumURLQ  string
	ChecksumNameQ string
	AgentConfig   string
	InitScript    string
}

func RenderAWGMBootstrapScript(p AWGMBootstrapParams) (string, error) {
	if p.Nickname == "" || p.BackendURL == "" || p.RawToken == "" {
		return "", fmt.Errorf("nickname, backend url, and raw token are required")
	}
	if p.DownloadURL == "" || p.ChecksumURL == "" || p.ChecksumName == "" {
		return "", fmt.Errorf("download url, checksum url, and checksum name are required")
	}
	agentYAML, err := RenderAgentYAML(AgentParams{
		BackendURL: p.BackendURL,
		Token:      p.RawToken,
		Nickname:   p.Nickname,
	})
	if err != nil {
		return "", err
	}
	initScript, err := ReadStaticTemplate("S99wg-monitor")
	if err != nil {
		return "", err
	}
	raw, err := renderTemplate("awgm-bootstrap.sh.tmpl", awgmBootstrapTemplateParams{
		NicknameQ:     shellQuote(p.Nickname),
		VersionQ:      shellQuote(p.Version),
		DownloadURLQ:  shellQuote(p.DownloadURL),
		ChecksumURLQ:  shellQuote(p.ChecksumURL),
		ChecksumNameQ: shellQuote(p.ChecksumName),
		AgentConfig:   strings.TrimRight(string(agentYAML), "\n"),
		InitScript:    strings.TrimRight(string(initScript), "\n"),
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
