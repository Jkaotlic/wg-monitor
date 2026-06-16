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
	ChecksumName string
	ExpectedSHA  string
	DeferStart   bool
}

type awgmBootstrapTemplateParams struct {
	NicknameQ     string
	VersionQ      string
	DownloadURLQ  string
	ChecksumNameQ string
	ExpectedSHAQ  string
	AgentConfig   string
	InitScript    string
	StartBlock    string
}

func RenderAWGMBootstrapScript(p AWGMBootstrapParams) (string, error) {
	if p.Nickname == "" || p.BackendURL == "" || p.RawToken == "" {
		return "", fmt.Errorf("nickname, backend url, and raw token are required")
	}
	if p.DownloadURL == "" || p.ChecksumName == "" || p.ExpectedSHA == "" {
		return "", fmt.Errorf("download url, checksum name, and expected sha are required")
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
		ChecksumNameQ: shellQuote(p.ChecksumName),
		ExpectedSHAQ:  shellQuote(p.ExpectedSHA),
		AgentConfig:   strings.TrimRight(string(agentYAML), "\n"),
		InitScript:    strings.TrimRight(string(initScript), "\n"),
		StartBlock:    awgmBootstrapStartBlock(!p.DeferStart),
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func awgmBootstrapStartBlock(start bool) string {
	if !start {
		return `echo "wg-monitor bootstrap staged; service start deferred until backend token commit"`
	}
	return awgmServiceStartBlock("wg-monitor bootstrap complete for $NICKNAME")
}

func RenderAWGMStartScript(nickname string) string {
	return "#!/bin/sh\nset -eu\nINIT=/opt/etc/init.d/S99wg-monitor\n" +
		awgmServiceStartBlock("wg-monitor service started for "+shellQuote(nickname)) + "\n"
}

func awgmServiceStartBlock(message string) string {
	return `"$INIT" restart || "$INIT" start
echo ` + message
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
