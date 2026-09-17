package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

func TestNewSelfHostedServiceMigratesYAMLPasswordWithWarning(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	path := filepath.Join(t.TempDir(), "amnezia-selfhosted.json")
	cfg := selfhostedamnezia.Config{
		Enabled: true, StorePath: path, EndpointHost: "vpn.example.com", EndpointPort: 47567,
		SSHHost: "203.0.113.7", SSHPassword: "SECRET-YAML-PASS-MUST-NOT-LEAK",
	}
	svc := newSelfHostedService(cfg, logger)
	insts, err := svc.List()
	if err != nil || len(insts) != 1 || insts[0].SSHPassword != cfg.SSHPassword {
		t.Fatalf("после переноса: %+v err=%v", insts, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл своих серверов не создан: %v", err)
	}
	if !strings.Contains(logs.String(), "удалите") || strings.Contains(logs.String(), "SECRET-YAML") {
		t.Fatalf("предупреждение: %s", logs.String())
	}

	logs.Reset()
	_ = newSelfHostedService(selfhostedamnezia.Config{StorePath: filepath.Join(t.TempDir(), "x.json")}, logger)
	if logs.Len() != 0 {
		t.Fatalf("без пароля в YAML предупреждать не о чем: %s", logs.String())
	}
}
