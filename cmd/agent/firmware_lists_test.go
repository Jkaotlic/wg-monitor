package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

func TestNoteRunningConfig_RecordsFirmwareSiteGroups(t *testing.T) {
	t.Cleanup(func() { firmwareSiteGroups.Store(0) })
	noteRunningConfig("dns-proxy\n!\nobject-group fqdn work\n    include example.com\n!\n")
	if got := firmwareSiteGroups.Load(); got != 1 {
		t.Fatalf("с группой: %d", got)
	}
	noteRunningConfig("dns-proxy\n!\n")
	if got := firmwareSiteGroups.Load(); got != 0 {
		t.Fatalf("без группы признак не снялся: %d", got)
	}
}

func TestBuildRunner_WiresFirmwareSiteGroups(t *testing.T) {
	t.Cleanup(func() { firmwareSiteGroups.Store(0) })
	cfg := &agent.Config{}
	awg := awgmgr.New("http://127.0.0.1:1")
	r := buildRunner(cfg, "/opt/etc/wg-monitor/config.yaml", awg, nil, nil, buildSingleChecks(cfg, awg, nil), nil)
	if r.FirmwareSiteGroups == nil {
		t.Fatal("FirmwareSiteGroups не подключён")
	}
	noteRunningConfig("object-group fqdn a\nobject-group fqdn b\n")
	if got := r.FirmwareSiteGroups(); got != 2 {
		t.Fatalf("got %d", got)
	}
}

type failingNDMC struct{}

func (failingNDMC) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("ndmc: timeout")
}

// v0.56, ревью I2: чтение running-config упало -- признак списков прошивки
// сбрасывается, а не держит число из прошлого чтения.
func TestReadDNSEndpoints_ErrorResetsFirmwareSiteGroups(t *testing.T) {
	t.Cleanup(func() { firmwareSiteGroups.Store(0) })
	firmwareSiteGroups.Store(3)
	if _, err := readDNSEndpointsVia(context.Background(), failingNDMC{}); err == nil {
		t.Fatal("ждали ошибку чтения")
	}
	if got := firmwareSiteGroups.Load(); got != 0 {
		t.Fatalf("после ошибки чтения признак держит устаревшее: %d", got)
	}
}

// v0.57: сброс DNS пробует эталон перед применением. Без этой проводки
// DNSProbe остался бы nil, и сброс молча писал бы мёртвые серверы.
func TestBuildRunner_WiresDNSProbe(t *testing.T) {
	cfg := &agent.Config{}
	awg := awgmgr.New("http://127.0.0.1:1")
	r := buildRunner(cfg, "/opt/etc/wg-monitor/config.yaml", awg, nil, nil, buildSingleChecks(cfg, awg, nil), nil)
	if r.DNSProbe == nil {
		t.Fatal("DNSProbe не подключён")
	}
}
