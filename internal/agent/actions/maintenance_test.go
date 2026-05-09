package actions

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

const ndmcComponentsListGolden_NoUpdate = "\x1b[K\n         firmware: \n              version: 5.00.C.11.0-0\n                title: 5.0.11\n\n          sandbox: stable\n\n            local: \n              sandbox: stable\n              version: 5.00.C.11.0-0\n"

const ndmcComponentsListGolden_HasUpdate = "\x1b[K\n         firmware: \n              version: 5.00.C.12.0-0\n                title: 5.0.12\n\n          sandbox: stable\n\n            local: \n              sandbox: stable\n              version: 5.00.C.11.0-0\n"

func TestParseComponentsList_NoUpdate(t *testing.T) {
	fs, err := parseComponentsList(ndmcComponentsListGolden_NoUpdate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "" {
		t.Errorf("Available=%q, expected empty when current==firmware", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_HasUpdate(t *testing.T) {
	fs, err := parseComponentsList(ndmcComponentsListGolden_HasUpdate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "5.00.C.12.0-0" {
		t.Errorf("Available=%q, want 5.00.C.12.0-0", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_CRLF(t *testing.T) {
	// Same content as NoUpdate golden but with CRLF line endings, as produced by
	// some SSH wrappers or Windows test fixtures.
	crlf := strings.ReplaceAll(ndmcComponentsListGolden_NoUpdate, "\n", "\r\n")
	fs, err := parseComponentsList(crlf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "" {
		t.Errorf("Available=%q, expected empty when current==firmware", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_MissingLocal(t *testing.T) {
	in := "         firmware: \n              version: 5.00.C.11.0-0\n          sandbox: stable\n"
	if _, err := parseComponentsList(in); err == nil {
		t.Error("expected error when local block is missing")
	}
}

func TestGetFirmwareStatus_ExecError(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, &execErr{msg: "boom"}
	}
	if _, err := GetFirmwareStatus(context.Background(), exec); err == nil {
		t.Fatal("expected error from exec failure")
	}
}

type execErr struct{ msg string }

func (e *execErr) Error() string { return e.msg }

func TestInstallFirmware_ExecCommand(t *testing.T) {
	var got [][]string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = append(got, append([]string{name}, args...))
		return []byte("ok"), nil
	}
	if err := InstallFirmware(context.Background(), exec); err != nil {
		t.Fatalf("InstallFirmware: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(got))
	}
	want := []string{"ndmc", "-c", "components commit"}
	if !slicesEq(got[0], want) {
		t.Errorf("exec=%v, want %v", got[0], want)
	}
}

func TestInstallFirmware_ExecError(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, &execErr{msg: "no such command"}
	}
	if err := InstallFirmware(context.Background(), exec); err == nil {
		t.Error("expected error from exec failure")
	}
}

func slicesEq(a, b []string) bool {
	if len(a) != len(b) { return false }
	for i := range a {
		if a[i] != b[i] { return false }
	}
	return true
}

// --- tests for VersionAudit ---

type fakeAwgInfo struct {
	sysInfo awgmgr.SystemInfo
	hrStat  *awgmgr.HydraRouteStatus
	sysErr  error
	hrErr   error
}

func (f *fakeAwgInfo) SystemInfo(ctx context.Context) (*awgmgr.SystemInfo, error) {
	if f.sysErr != nil { return nil, f.sysErr }
	return &f.sysInfo, nil
}
func (f *fakeAwgInfo) HydraRouteStatus(ctx context.Context) (*awgmgr.HydraRouteStatus, error) {
	if f.hrErr != nil { return nil, f.hrErr }
	return f.hrStat, nil
}

const opkgInfoHrneoGolden = "Package: hrneo\nVersion: 2.4.0-1\nDepends: libc, ipset, iptables\nStatus: install user installed\n"
const procUptimeGolden     = "105561.18 193126.14\n"
const procStatHrneoGolden  = "20604 (hrneo) S 1 20602 1874 0 -1 4194560 62568 896951 0 0 278 1662 453 365 20 0 1 0 4118135 3846144 725\n"
const procStatAwgmgrGolden = "1895 (awg-manager) S 1 1894 1874 0 -1 4194560 50000 800000 0 0 200 1500 400 300 20 0 1 0 700000 3846144 600\n"

func TestParseHrneoOpkg(t *testing.T) {
	v, err := parseHrneoOpkg(opkgInfoHrneoGolden)
	if err != nil { t.Fatal(err) }
	if v != "2.4.0" {
		t.Errorf("got %q, want 2.4.0 (suffix -1 must be stripped)", v)
	}
}

func TestParseHrneoOpkg_NoVersion(t *testing.T) {
	if _, err := parseHrneoOpkg("Package: hrneo\nStatus: install user installed\n"); err == nil {
		t.Error("expected error when Version line missing")
	}
}

func TestHumanizeUptime(t *testing.T) {
	cases := map[int64]string{
		0:                 "0с",
		42:                "42с",
		83:                "1м 23с",
		5*60 + 30:         "5м 30с",
		3600 + 30*60:      "1ч 30м",
		25*3600 + 4*3600:  "1д 5ч",      // 29 hours = 1d 5h
		3*86400 + 4*3600:  "3д 4ч",
		7*86400 + 12*3600: "7д 12ч",
	}
	for sec, want := range cases {
		if got := humanizeUptime(sec); got != want {
			t.Errorf("humanizeUptime(%d)=%q, want %q", sec, got, want)
		}
	}
}

func TestDaemonUptime_OK(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "pidof" && len(args) == 1 && args[0] == "hrneo":
			return []byte("20604\n"), nil
		case name == "cat" && len(args) == 1 && args[0] == "/proc/20604/stat":
			return []byte(procStatHrneoGolden), nil
		}
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	const sysUp = 105561.18
	got := daemonUptime(context.Background(), exec, sysUp, "hrneo")
	// daemon_uptime = 105561 - (4118135/100) = 105561 - 41181 = 64380 sec ≈ 17h 53m
	// 64380 / 3600 = 17.88 → "17ч 53м"
	if got != "17ч 53м" {
		t.Errorf("got %q, want \"17ч 53м\"", got)
	}
}

func TestDaemonUptime_NotRunning(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("not running")
	}
	if got := daemonUptime(context.Background(), exec, 1000.0, "missing"); got != "" {
		t.Errorf("expected empty when daemon missing, got %q", got)
	}
}

func TestVersionAudit_AllFields(t *testing.T) {
	awg := &fakeAwgInfo{
		sysInfo: awgmgr.SystemInfo{Version: "2.8.2", FirmwareVersion: "5.00.C.11.0-0"},
		hrStat:  &awgmgr.HydraRouteStatus{Installed: true, Running: true},
	}
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ndmc" && len(args) == 2 && args[1] == "components list":
			return []byte(ndmcComponentsListGolden_NoUpdate), nil
		case name == "opkg" && len(args) == 2 && args[0] == "info" && args[1] == "hrneo":
			return []byte(opkgInfoHrneoGolden), nil
		case name == "cat" && len(args) == 1 && args[0] == "/proc/uptime":
			return []byte(procUptimeGolden), nil
		case name == "pidof" && len(args) == 1 && args[0] == "hrneo":
			return []byte("20604\n"), nil
		case name == "pidof" && len(args) == 1 && args[0] == "awg-manager":
			return []byte("1895\n"), nil
		case name == "cat" && len(args) == 1 && args[0] == "/proc/20604/stat":
			return []byte(procStatHrneoGolden), nil
		case name == "cat" && len(args) == 1 && args[0] == "/proc/1895/stat":
			return []byte(procStatAwgmgrGolden), nil
		}
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	got, err := VersionAudit(context.Background(), awg, exec)
	if err != nil { t.Fatalf("VersionAudit: %v", err) }
	if got.AwgmgrVersion != "2.8.2" { t.Errorf("AwgmgrVersion=%q", got.AwgmgrVersion) }
	if got.HrneoVersion  != "2.4.0" { t.Errorf("HrneoVersion=%q",  got.HrneoVersion) }
	if got.FirmwareCurrent != "5.00.C.11.0-0" { t.Errorf("FirmwareCurrent=%q", got.FirmwareCurrent) }
	if got.FirmwareAvail   != "" { t.Errorf("FirmwareAvail=%q, expected empty (no update in golden)", got.FirmwareAvail) }
	if got.HrneoUptime  != "17ч 53м" { t.Errorf("HrneoUptime=%q",  got.HrneoUptime) }
	if got.AwgmgrUptime != "1д 3ч" {
		t.Errorf("AwgmgrUptime=%q, want 1д 3ч", got.AwgmgrUptime)
	}
}

func TestVersionAudit_HrneoNotInstalled(t *testing.T) {
	awg := &fakeAwgInfo{
		sysInfo: awgmgr.SystemInfo{Version: "2.8.2", FirmwareVersion: "5.00.C.11.0-0"},
		hrStat:  &awgmgr.HydraRouteStatus{Installed: false, Running: false},
	}
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ndmc": return []byte(ndmcComponentsListGolden_NoUpdate), nil
		case name == "cat" && args[0] == "/proc/uptime": return []byte(procUptimeGolden), nil
		case name == "pidof" && args[0] == "awg-manager": return []byte("1895\n"), nil
		case name == "cat" && args[0] == "/proc/1895/stat": return []byte(procStatAwgmgrGolden), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	got, err := VersionAudit(context.Background(), awg, exec)
	if err != nil { t.Fatalf("VersionAudit: %v", err) }
	if got.HrneoVersion != "" { t.Errorf("expected empty HrneoVersion when !Installed, got %q", got.HrneoVersion) }
	if got.HrneoUptime  != "" { t.Errorf("expected empty HrneoUptime when !Installed, got %q",  got.HrneoUptime) }
}

func TestEncodeVersionAudit_RoundTrip(t *testing.T) {
	in := wire.VersionAudit{AwgmgrVersion: "2.8.2", FirmwareCurrent: "5.0.0", HrneoVersion: "2.4.0"}
	s, err := EncodeVersionAudit(in)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(s, `"awgmgr_version":"2.8.2"`) {
		t.Errorf("encoded missing field: %s", s)
	}
}

func TestVersionAudit_SysInfoError(t *testing.T) {
	awg := &fakeAwgInfo{sysErr: fmt.Errorf("connection refused")}
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("should not be called")
	}
	_, err := VersionAudit(context.Background(), awg, exec)
	if err == nil {
		t.Fatal("expected non-nil error when SystemInfo fails")
	}
	if !strings.Contains(err.Error(), "SystemInfo") {
		t.Errorf("error should mention SystemInfo, got: %v", err)
	}
}

func TestParseProcStatStarttime(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
		ok   bool
	}{
		{
			name: "simple",
			in:   "1234 (myproc) S 1 1234 1234 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 555000 1024 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0",
			want: 555000,
			ok:   true,
		},
		{
			name: "name with spaces and parens",
			in:   "999 ((weird) name)) R 1 999 999 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 12345 1024 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0",
			want: 12345,
			ok:   true,
		},
		{
			name: "no closing paren",
			in:   "1234 myproc S 1 1234",
			ok:   false,
		},
		{
			name: "too few fields after )",
			in:   "1 (x) S 1 2 3",
			ok:   false,
		},
	}
	for _, c := range cases {
		got, ok := parseProcStatStarttime(c.in)
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%s: got=%d want %d", c.name, got, c.want)
		}
	}
}
