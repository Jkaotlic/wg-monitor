package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/wakehook"
)

func TestBuildSignalsInstallsHookAndWiresHub(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "run", "wg-monitor.wake")
	s := buildSignals(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"), dir, wake)
	if s.hookState != wakehook.StateInstalled {
		t.Fatalf("state = %q", s.hookState)
	}
	if _, err := os.Stat(filepath.Join(dir, wakehook.ScriptName)); err != nil {
		t.Fatal("хук не поставлен")
	}
	if h := s.hub.Hooks(); h == nil || h.State != wakehook.StateInstalled {
		t.Fatalf("блок hooks = %+v", h)
	}
	if s.hub.Exit == nil || s.hub.Ping == nil || s.hub.WAN == nil {
		t.Fatal("сборщик фактов не подключён")
	}
	if s.wan.PingCheck == nil {
		t.Fatal("хук Ping-Check (Task 15a) не подключён к wanfacts.Collector")
	}
}

// На машине без /bin/ndmc (любой не-KeeneticOS хост, включая CI и этот Mac)
// хук обязан вернуться с ошибкой, а не паникой -- то же вырождение, что и
// при нечитаемом running-config на самом роутере.
func TestBuildSignalsPingCheckHookDegradesWithoutNDMC(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "run", "wg-monitor.wake")
	s := buildSignals(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"), dir, wake)
	profiles, err := s.wan.PingCheck(context.Background())
	if err == nil {
		t.Fatal("ожидали ошибку -- ndmc недоступен на этой машине")
	}
	if profiles != nil {
		t.Fatalf("profiles = %+v, want nil при ошибке", profiles)
	}
}

func TestBuildSignalsHonoursWakeHooksOff(t *testing.T) {
	dir := t.TempDir()
	wake := filepath.Join(t.TempDir(), "w")
	cli := awgmgr.New("http://127.0.0.1:1")
	buildSignals(&agent.Config{}, cli, dir, wake)
	cfg := &agent.Config{}
	cfg.Agent.WakeHooksOff = true
	s := buildSignals(cfg, cli, dir, wake)
	if s.hookState != wakehook.StateDisabled {
		t.Fatalf("state = %q", s.hookState)
	}
	if _, err := os.Stat(filepath.Join(dir, wakehook.ScriptName)); !os.IsNotExist(err) {
		t.Fatal("выключенный хук остался в каталоге")
	}
}
