package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeHrneoOpkg -- роутер с Entware: opkg, df, init.d и pidof по сценарию.
type fakeHrneoOpkg struct {
	calls      []string
	installed  string // версия до обновления; "" = не установлен
	after      string // версия после opkg upgrade hrneo
	upgradable string
	dfOut      string
	running    bool
	upgraded   bool
}

const dfPlenty = "Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/sda1 1000000 500000 500000 50% /opt\n"

func (f *fakeHrneoOpkg) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	switch call {
	case "opkg info hrneo":
		v := f.installed
		if f.upgraded {
			v = f.after
		}
		if v == "" {
			return []byte(""), nil
		}
		return []byte("Package: hrneo\nVersion: " + v + "\nStatus: install user installed\nInstalled-Size: 2048000\n"), nil
	case "opkg update":
		return []byte("Updated list of available packages in /opt/var/opkg-lists/entware\n"), nil
	case "opkg list-upgradable":
		return []byte(f.upgradable), nil
	case "df -k /opt":
		return []byte(f.dfOut), nil
	case "opkg upgrade hrneo":
		f.upgraded = true
		return []byte("Upgrading hrneo on root from " + f.installed + " to " + f.after + "...\n"), nil
	case "/opt/etc/init.d/S99hrneo restart":
		return []byte("restarted\n"), nil
	case "pidof hrneo":
		if f.running {
			return []byte("1234\n"), nil
		}
		return nil, errors.New("exit status 1")
	}
	return nil, fmt.Errorf("unexpected exec %q", call)
}

func hrneoRunner(t *testing.T, f *fakeHrneoOpkg) *OpkgRunner {
	t.Helper()
	o := mkOpkgRunner(t, f.exec)
	o.Sleep = func(context.Context, time.Duration) error { return nil }
	return o
}

func TestHrneoUpdate_NotUpgradableIsLatest(t *testing.T) {
	f := &fakeHrneoOpkg{installed: "3.18.3-1", upgradable: "libc - 1.0 - 1.1\n", dfOut: dfPlenty, running: true}
	status, out := hrneoRunner(t, f).HrneoUpdate(context.Background())
	if status != "ok" {
		t.Fatalf("status=%q out=%q", status, out)
	}
	var res wire.HrneoUpdateResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res != (wire.HrneoUpdateResult{Updated: false, From: "3.18.3-1", To: "3.18.3-1", Running: true}) {
		t.Errorf("res = %+v", res)
	}
	for _, c := range f.calls {
		if c == "opkg upgrade hrneo" || strings.HasPrefix(c, "/opt/etc/init.d/S99hrneo") {
			t.Errorf("обновлять нечего, а вызвано %q", c)
		}
	}
}

func TestHrneoUpdate_UpgradesAndRestarts(t *testing.T) {
	f := &fakeHrneoOpkg{installed: "3.18.3-1", after: "3.19.0-1", upgradable: "hrneo - 3.18.3-1 - 3.19.0-1\n", dfOut: dfPlenty, running: true}
	status, out := hrneoRunner(t, f).HrneoUpdate(context.Background())
	if status != "ok" {
		t.Fatalf("status=%q out=%q", status, out)
	}
	var res wire.HrneoUpdateResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res != (wire.HrneoUpdateResult{Updated: true, From: "3.18.3-1", To: "3.19.0-1", Running: true}) {
		t.Errorf("res = %+v", res)
	}
	joined := strings.Join(f.calls, "|")
	up := strings.Index(joined, "opkg upgrade hrneo")
	restart := strings.Index(joined, "/opt/etc/init.d/S99hrneo restart")
	if !strings.HasPrefix(joined, "opkg info hrneo|opkg update|opkg list-upgradable") || up < 0 || restart < up {
		t.Errorf("порядок вызовов: %v", f.calls)
	}
}

func TestHrneoUpdate_NoSpaceRefusesBeforeUpgrade(t *testing.T) {
	f := &fakeHrneoOpkg{
		installed: "3.18.3-1", after: "3.19.0-1", upgradable: "hrneo - 3.18.3-1 - 3.19.0-1\n", running: true,
		dfOut: "Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/sda1 1000000 899000 101000 90% /opt\n",
	}
	status, out := hrneoRunner(t, f).HrneoUpdate(context.Background())
	if status != "err" || !strings.Contains(out, "Не хватит места") {
		t.Fatalf("status=%q out=%q", status, out)
	}
	for _, c := range f.calls {
		if c == "opkg upgrade hrneo" {
			t.Error("места нет, а upgrade всё равно вызван")
		}
	}
}

func TestHrneoUpdate_LockBusy(t *testing.T) {
	f := &fakeHrneoOpkg{installed: "3.18.3-1"}
	o := hrneoRunner(t, f)
	if err := os.WriteFile(o.LockPath, []byte("pid=other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, out := o.HrneoUpdate(context.Background())
	if status != "locked" || len(f.calls) != 0 {
		t.Errorf("status=%q calls=%v", status, f.calls)
	}
	if out != "на роутере уже идёт другая операция с пакетами — повторите через пару минут" {
		t.Errorf("lock text should be Russian and owner-facing, got %q", out)
	}
	if strings.Contains(out, "opkg") || strings.Contains(out, "lock file") {
		t.Errorf("lock text must not leak internal opkg/lock-file names: %q", out)
	}
}

func TestHrneoUpdate_NotInstalled(t *testing.T) {
	f := &fakeHrneoOpkg{}
	status, out := hrneoRunner(t, f).HrneoUpdate(context.Background())
	if status != "err" || out != "HydraRoute Neo не установлен" {
		t.Errorf("status=%q out=%q", status, out)
	}
}

func TestHrneoUpdate_NotRunningAfterRestartIsError(t *testing.T) {
	f := &fakeHrneoOpkg{installed: "3.18.3-1", after: "3.19.0-1", upgradable: "hrneo - 3.18.3-1 - 3.19.0-1\n", dfOut: dfPlenty, running: false}
	status, out := hrneoRunner(t, f).HrneoUpdate(context.Background())
	if status != "err" || !strings.Contains(out, "не запустился") {
		t.Errorf("status=%q out=%q", status, out)
	}
}

func TestInstalledVersionFromOpkgInfo(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"installed", "Package: hrneo\nVersion: 3.18.3-1\nStatus: install user installed\n", "3.18.3-1", true},
		{"only feed candidate", "Package: hrneo\nVersion: 3.19.0-1\nStatus: unknown ok not-installed\n", "", false},
		{"feed then installed", "Package: hrneo\nVersion: 3.19.0-1\nStatus: unknown ok not-installed\n\nPackage: hrneo\nVersion: 3.18.3-1\nStatus: install ok installed\n", "3.18.3-1", true},
		{"no status line", "Package: hrneo\nVersion: 3.18.3-1\n", "3.18.3-1", true},
		{"empty", "", "", false},
	} {
		got, ok := installedVersionFromOpkgInfo(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSpaceVerdict(t *testing.T) {
	if ok, head := spaceVerdict(500000, 1000000, 2000); !ok || head != 100000 {
		t.Errorf("хватает: ok=%v head=%d", ok, head)
	}
	if ok, _ := spaceVerdict(101000, 1000000, 2000); ok {
		t.Error("99000 после установки меньше 10% от 1000000 -- места не хватает")
	}
}

func TestRunner_HrneoUpdate_Dispatch(t *testing.T) {
	stub := stubOpkg{hrneoFn: func(ctx context.Context) (string, string) { return "ok", `{"updated":false}` }}
	res := (&Runner{Opkg: &stub}).Execute(context.Background(), wire.Command{ID: "h1", Action: "hrneo_update"})
	if res.Status != "ok" || res.Output != `{"updated":false}` {
		t.Errorf("res = %+v", res)
	}
	if got := actionTimeoutFor("hrneo_update"); got != 300*time.Second {
		t.Errorf("hrneo_update budget = %v, want 300s", got)
	}
}
