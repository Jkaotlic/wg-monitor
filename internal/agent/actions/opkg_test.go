package actions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mkOpkgRunner(t *testing.T, exec ExecFunc) *OpkgRunner {
	t.Helper()
	dir := t.TempDir()
	return &OpkgRunner{
		LockPath: filepath.Join(dir, "opkg.lock"),
		LockTTL:  100 * time.Millisecond,
		Exec:     exec,
		Now:      time.Now,
	}
}

// Свежий замок: «занято» словами для человека, без пути файла и имён opkg.
func TestOpkg_SmartUpgradeAndDisableFeed_LockedTextIsRussian(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("opkg should not be invoked when locked")
		return nil, nil
	})
	if err := os.WriteFile(o.LockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	status, output, _ := o.SmartUpgrade(context.Background())
	status2, output2, _ := o.DisableFeed(context.Background(), "https://feed.example.com/a")
	for _, c := range []struct{ s, o string }{{status, output}, {status2, output2}} {
		if c.s != "locked" || !strings.Contains(c.o, "повторите") {
			t.Errorf("status=%q output=%q", c.s, c.o)
		}
		if strings.Contains(c.o, "opkg") || strings.Contains(c.o, "lock file") || strings.Contains(c.o, o.LockPath) {
			t.Errorf("текст замка выдаёт внутренние имена: %q", c.o)
		}
	}
}

func TestOpkg_TakeLockRefusesExistingLock(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("opkg should not be invoked when lock acquisition fails")
		return nil, nil
	})
	if err := os.WriteFile(o.LockPath, []byte("pid=other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := o.takeLock()
	if err == nil {
		t.Fatal("expected takeLock to refuse an existing lock")
	}
	body, readErr := os.ReadFile(o.LockPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "pid=other\n" {
		t.Fatalf("existing lock was overwritten: %q", body)
	}
}

// Real-world opkg update output when one of five feeds is dead (HTTP 404).
// opkg exits 1 even though four feeds downloaded successfully — historically
// SmartUpgrade treated this as total failure, blocking the upgrade.
const partialUpdateOutput = `Downloading http://bin.entware.net/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/entware
Downloading http://bin.entware.net/aarch64-k3.10/keenetic/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/keendev
Downloading http://repo.hoaxisr.ru/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/hoaxisr
Downloading https://git.zerrolabs.org/Ground-Zerro/release/pages/keenetic/aarch64-k3.10/Packages.gz
Updated list of available packages in /opt/var/opkg-lists/ground-zerro
Downloading https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz
*** Failed to download the package list from https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz

Collected errors:
 * opkg_download: Failed to download https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz, wget returned 8.
`

func TestParseOpkgUpdate_PartialFailure(t *testing.T) {
	got := parseOpkgUpdate(partialUpdateOutput)
	if got.feedsUpdated != 4 {
		t.Errorf("feedsUpdated = %d, want 4", got.feedsUpdated)
	}
	if len(got.failedFeeds) != 1 {
		t.Fatalf("failedFeeds = %v, want 1 entry", got.failedFeeds)
	}
	if got.failedFeeds[0] != "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz" {
		t.Errorf("failedFeeds[0] = %q", got.failedFeeds[0])
	}
}

func TestParseOpkgUpdate_AllSuccess(t *testing.T) {
	out := "Downloading http://x/Packages.gz\nUpdated list of available packages in /opt/var/opkg-lists/x\n"
	got := parseOpkgUpdate(out)
	if got.feedsUpdated != 1 || len(got.failedFeeds) != 0 {
		t.Errorf("got %+v, want feedsUpdated=1, failedFeeds=[]", got)
	}
}

func TestParseOpkgUpdate_TotalFailure(t *testing.T) {
	out := "Downloading http://x/Packages.gz\n*** Failed to download the package list from http://x/Packages.gz\n"
	got := parseOpkgUpdate(out)
	if got.feedsUpdated != 0 || len(got.failedFeeds) != 1 {
		t.Errorf("got %+v, want feedsUpdated=0, failedFeeds=[1]", got)
	}
}

func TestOpkg_SmartUpgrade_PartialUpdateFailure_Continues(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "update":
			return []byte(partialUpdateOutput), errors.New("exit status 1")
		case "list-upgradable":
			return []byte(""), nil // empty → SmartUpgrade exits early with "up to date"
		}
		return nil, nil
	})
	status, output, payload := o.SmartUpgrade(context.Background())
	if status != "ok" {
		t.Fatalf("status=%q, want ok; output=%q", status, output)
	}
	if !strings.Contains(output, "anonym-tsk.github.io") {
		t.Errorf("output should surface dead URL; got %q", output)
	}
	if len(payload.FailedFeeds) != 1 || payload.FailedFeeds[0] != "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz" {
		t.Errorf("payload.FailedFeeds = %v", payload.FailedFeeds)
	}
}

func TestOpkg_SmartUpgrade_TotalUpdateFailure_Errs(t *testing.T) {
	totalFail := `Downloading http://bin.entware.net/aarch64-k3.10/Packages.gz
*** Failed to download the package list from http://bin.entware.net/aarch64-k3.10/Packages.gz
`
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte(totalFail), errors.New("exit status 1")
		}
		return nil, nil
	})
	status, _, _ := o.SmartUpgrade(context.Background())
	if status != "err" {
		t.Fatalf("status=%q, want err", status)
	}
}

func TestOpkgDfOptRejectsNonNumericFields(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "df" || strings.Join(args, " ") != "-k /opt" {
			t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/root nope 10 also-nope 20% /opt\n"), nil
	})

	if _, _, err := o.dfOpt(context.Background()); err == nil {
		t.Fatal("dfOpt should reject non-numeric total/free fields")
	}
}

func TestNormalizeFeedURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"https://anonym-tsk.github.io/nfqws-keenetic/all/", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"https://anonym-tsk.github.io/nfqws-keenetic/all", "https://anonym-tsk.github.io/nfqws-keenetic/all"},
		{"http://bin.entware.net/aarch64-k3.10", "http://bin.entware.net/aarch64-k3.10"},
		{"https://x.example/Packages.gz/Packages.gz", "https://x.example/Packages.gz"},
	}
	for _, c := range cases {
		got := normalizeFeedURL(c.in)
		if got != c.want {
			t.Errorf("normalizeFeedURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDisableMatchingLine_SimpleMatch(t *testing.T) {
	body := []byte("src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	url := "https://anonym-tsk.github.io/nfqws-keenetic/all"
	out, hit := disableMatchingLine(body, url, "2026-05-12T10:00:00Z")
	if !hit {
		t.Fatalf("expected hit")
	}
	want := "# disabled by wg-monitor 2026-05-12T10:00:00Z: src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n"
	if string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestDisableMatchingLine_MultiFeed_OnlyTargetCommented(t *testing.T) {
	body := []byte("src/gz entware http://bin.entware.net/aarch64-k3.10\n" +
		"src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n" +
		"src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n")
	url := "https://anonym-tsk.github.io/nfqws-keenetic/all"
	out, hit := disableMatchingLine(body, url, "T")
	if !hit {
		t.Fatalf("expected hit")
	}
	s := string(out)
	if !strings.Contains(s, "src/gz entware http://bin.entware.net/aarch64-k3.10\n") {
		t.Errorf("entware line should be untouched, got %q", s)
	}
	if !strings.Contains(s, "# disabled by wg-monitor T: src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n") {
		t.Errorf("nfqws line should be commented, got %q", s)
	}
	if !strings.Contains(s, "src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n") {
		t.Errorf("hoaxisr line should be untouched, got %q", s)
	}
}

func TestDisableMatchingLine_SkipsAlreadyCommented(t *testing.T) {
	body := []byte("# src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if hit {
		t.Errorf("commented line must not be re-disabled")
	}
	if string(out) != string(body) {
		t.Errorf("body must be unchanged, got %q", out)
	}
}

func TestDisableMatchingLine_NoMatch(t *testing.T) {
	body := []byte("src/gz entware http://bin.entware.net/aarch64-k3.10\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if hit {
		t.Errorf("should not match")
	}
	if string(out) != string(body) {
		t.Errorf("body must be unchanged")
	}
}

func TestDisableMatchingLine_SrcWithoutGz(t *testing.T) {
	// Some feeds use `src` (no /gz) for uncompressed Packages files.
	body := []byte("src nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	out, hit := disableMatchingLine(body, "https://anonym-tsk.github.io/nfqws-keenetic/all", "T")
	if !hit {
		t.Fatalf("expected hit on `src` variant")
	}
	if !strings.HasPrefix(string(out), "# disabled by wg-monitor T:") {
		t.Errorf("expected comment prefix, got %q", out)
	}
}

// mkOpkgRunnerWithRoot is a variant of mkOpkgRunner that points ConfigRoot at
// a temp dir holding an opkg.conf and/or opkg/<feed>.conf files. Used by
// DisableFeed tests.
func mkOpkgRunnerWithRoot(t *testing.T, root string, exec ExecFunc) *OpkgRunner {
	t.Helper()
	r := mkOpkgRunner(t, exec)
	r.ConfigRoot = root
	return r
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpkg_DisableFeed_PerFeedFile(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg", "nfqws.conf")
	writeFile(t, confPath, "src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	writeFile(t, filepath.Join(root, "opkg.conf"), "")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("Updated list of available packages in /opt/var/opkg-lists/x\n"), nil
		}
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return nil, nil
	})

	status, output, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all/Packages.gz")
	if status != "ok" {
		t.Fatalf("status=%q output=%q", status, output)
	}
	body, _ := os.ReadFile(confPath)
	if !strings.Contains(string(body), "# disabled by wg-monitor") {
		t.Errorf("expected comment line in %s, got %q", confPath, body)
	}
	matches, _ := filepath.Glob(confPath + ".bak.*")
	if len(matches) != 1 {
		t.Errorf("expected 1 backup file, got %v", matches)
	}
}

func TestOpkg_DisableFeed_MultiFeedFile(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg.conf")
	writeFile(t, confPath, "src/gz entware http://bin.entware.net/aarch64-k3.10\n"+
		"src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n"+
		"src/gz hoaxisr http://repo.hoaxisr.ru/aarch64-k3.10\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("Updated list of available packages in /opt/var/opkg-lists/x\n"), nil
		}
		return nil, nil
	})

	status, _, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Fatalf("status=%q", status)
	}
	body, _ := os.ReadFile(confPath)
	s := string(body)
	if !strings.Contains(s, "src/gz entware http://bin.entware.net/aarch64-k3.10\n") {
		t.Errorf("entware untouched: %q", s)
	}
	if !strings.Contains(s, "# disabled by wg-monitor") || !strings.Contains(s, "src/gz nfqws") {
		t.Errorf("nfqws not commented: %q", s)
	}
}

func TestOpkg_DisableFeed_DoesNotRewriteAnyFileWhenLaterConfigReadFails(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg.conf")
	original := "src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n"
	writeFile(t, confPath, original)
	if err := os.MkdirAll(filepath.Join(root, "opkg", "bad.conf"), 0o755); err != nil {
		t.Fatal(err)
	}

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("opkg should not run after config read failure: %s %v", name, args)
		return nil, nil
	})

	status, output, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "err" || !strings.Contains(output, "read") {
		t.Fatalf("status=%q output=%q, want read error", status, output)
	}
	body, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("opkg.conf was partially rewritten after later read failure:\n%s", body)
	}
	matches, _ := filepath.Glob(confPath + ".bak.*")
	if len(matches) != 0 {
		t.Fatalf("no backup should be created when planning fails, got %v", matches)
	}
}

func TestOpkg_DisableFeed_Idempotent(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg", "nfqws.conf")
	writeFile(t, confPath, "# src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")
	writeFile(t, filepath.Join(root, "opkg.conf"), "")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})

	status, output, _ := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Errorf("status=%q output=%q (idempotent should be ok)", status, output)
	}
	if !strings.Contains(output, "уже отключён") && !strings.Contains(output, "не найден") {
		t.Errorf("output should explain no-op, got %q", output)
	}
	matches, _ := filepath.Glob(confPath + ".bak.*")
	if len(matches) != 0 {
		t.Errorf("no backup should be created on no-op, got %v", matches)
	}
}

func TestOpkg_DisableFeed_NotFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "opkg.conf"), "src/gz entware http://bin.entware.net/aarch64-k3.10\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, nil
	})

	status, _, _ := o.DisableFeed(context.Background(), "https://nowhere.example/Packages.gz")
	if status != "ok" {
		t.Errorf("status=%q, want ok (no-op)", status)
	}
}

func TestOpkg_DisableFeed_InvalidURL(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "opkg.conf"), "")
	called := 0
	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called++
		return nil, nil
	})
	for _, bad := range []string{"", "ftp://x", "javascript:alert(1)", "/etc/passwd"} {
		status, _, _ := o.DisableFeed(context.Background(), bad)
		if status != "err" {
			t.Errorf("DisableFeed(%q) status=%q, want err", bad, status)
		}
	}
	if called != 0 {
		t.Errorf("DisableFeed should not invoke exec for invalid URLs; called=%d", called)
	}
}

func TestOpkg_DisableFeed_ThenSmartUpgrade(t *testing.T) {
	root := t.TempDir()
	confPath := filepath.Join(root, "opkg.conf")
	writeFile(t, confPath, "src/gz nfqws https://anonym-tsk.github.io/nfqws-keenetic/all\n")

	o := mkOpkgRunnerWithRoot(t, root, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "update":
			return []byte("Updated list of available packages in /opt/var/opkg-lists/entware\n"), nil
		case "list-upgradable":
			return []byte(""), nil
		}
		return nil, nil
	})

	status, output, payload := o.DisableFeed(context.Background(), "https://anonym-tsk.github.io/nfqws-keenetic/all")
	if status != "ok" {
		t.Fatalf("status=%q", status)
	}
	if !strings.Contains(output, "🔧 Отключён фид") {
		t.Errorf("combined output should start with disable header, got %q", output)
	}
	if !strings.Contains(output, "Все пакеты актуальны") {
		t.Errorf("combined output should include SmartUpgrade body, got %q", output)
	}
	if len(payload.FailedFeeds) != 0 {
		t.Errorf("payload.FailedFeeds should be empty after repair; got %v", payload.FailedFeeds)
	}
}

// AGENT-14: срок действия команды (300 с) убивал `opkg upgrade` SIGKILL'ом
// посреди установки -- пакеты оставались наполовину распакованными. Теперь
// установка не привязана к сроку команды (у неё свой потолок), а
// обновление списков и оценка места -- по-прежнему.
func TestOpkg_SmartUpgrade_UpgradeSurvivesActionDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var upgradeCtxErr error
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "opkg" && args[0] == "update":
			return []byte("Updated list of available packages in /opt/var/opkg-lists/entware\n"), nil
		case name == "opkg" && args[0] == "list-upgradable":
			return []byte("curl - 8.1 - 8.2\n"), nil
		case name == "opkg" && args[0] == "info":
			return []byte("Package: curl\nInstalled-Size: 100\n"), nil
		case name == "df":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 100000 50% /opt\n"), nil
		case name == "opkg" && args[0] == "upgrade":
			cancel() // срок команды истёк посреди установки
			time.Sleep(20 * time.Millisecond)
			upgradeCtxErr = ctx.Err()
			return []byte("Upgrading curl on root from 8.1 to 8.2...\n"), nil
		}
		return nil, nil
	})
	status, out, _ := o.SmartUpgrade(parent)
	if upgradeCtxErr != nil {
		t.Fatalf("opkg upgrade was cancelled by the action deadline (%v): a SIGKILL mid-install", upgradeCtxErr)
	}
	if status != "ok" {
		t.Fatalf("status=%q out=%q", status, out)
	}
}

// AGENT-14: cron-обновление (скрипт opkg_cron) берёт mkdir-замок в /tmp, а
// агент -- свой файл. Два opkg одновременно ломают базу пакетов. Теперь
// агент берёт и общий замок cron: занят -- "locked", opkg не запускается.
func TestOpkg_SmartUpgrade_RespectsCronLock(t *testing.T) {
	called := false
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	o.SharedLockDir = filepath.Join(t.TempDir(), "wg-monitor-opkg-auto-upgrade.lock")
	if err := os.Mkdir(o.SharedLockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	status, out, _ := o.SmartUpgrade(context.Background())
	if status != "locked" {
		t.Fatalf("status=%q, want locked while cron upgrade runs; out=%q", status, out)
	}
	if called {
		t.Fatal("opkg ran while the cron upgrade holds the lock")
	}
	if _, err := os.Stat(o.SharedLockDir); err != nil {
		t.Fatal("the cron lock must be left to its owner")
	}

	// Свободен -- агент берёт его на время работы и отпускает.
	_ = os.Remove(o.SharedLockDir)
	var heldDuring bool
	o.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, err := os.Stat(o.SharedLockDir)
		heldDuring = heldDuring || err == nil
		return nil, nil
	}
	if st, out, _ := o.SmartUpgrade(context.Background()); st != "ok" {
		t.Fatalf("status=%q out=%q", st, out)
	}
	if !heldDuring {
		t.Fatal("agent did not hold the shared cron lock while running opkg")
	}
	if _, err := os.Stat(o.SharedLockDir); !os.IsNotExist(err) {
		t.Fatal("shared cron lock not released")
	}
}

// Общий замок работает, только пока путь у агента и в cron-скрипте один.
func TestOpkgCronScriptUsesSharedLockDir(t *testing.T) {
	m := &OpkgCronManager{}
	if !strings.Contains(m.scriptText(), "LOCK="+OpkgCronSharedLockDir+"\n") {
		t.Fatalf("cron script lock differs from OpkgCronSharedLockDir %q", OpkgCronSharedLockDir)
	}
}

// AGENT-14 (ревью): замок в /tmp, брошенный после kill -9, висел до
// перезагрузки или двух часов. Теперь владелец пишет в него свой pid:
// владелец мёртв -- замок снимается и берётся; жив -- «занято»; pid нет
// (старый cron-скрипт) и замок свежий -- честно «владелец неизвестен».
func TestOpkg_SharedLockOwnerPid(t *testing.T) {
	newRunner := func(t *testing.T) (*OpkgRunner, *bool) {
		ran := false
		o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ran = true
			return nil, nil
		})
		o.SharedLockDir = filepath.Join(t.TempDir(), "wg-monitor-opkg-auto-upgrade.lock")
		return o, &ran
	}
	lockWithPid := func(t *testing.T, dir string, pid int) {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if pid > 0 {
			if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("owner alive", func(t *testing.T) {
		o, ran := newRunner(t)
		lockWithPid(t, o.SharedLockDir, os.Getpid())
		st, out, _ := o.SmartUpgrade(context.Background())
		if st != "locked" || *ran || !strings.Contains(out, "идёт") {
			t.Fatalf("status=%q ran=%v out=%q", st, *ran, out)
		}
	})
	t.Run("owner dead", func(t *testing.T) {
		o, ran := newRunner(t)
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		lockWithPid(t, o.SharedLockDir, cmd.Process.Pid)
		var pidDuring string
		o.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			*ran = true
			b, _ := os.ReadFile(filepath.Join(o.SharedLockDir, "pid"))
			pidDuring = strings.TrimSpace(string(b))
			return nil, nil
		}
		if st, out, _ := o.SmartUpgrade(context.Background()); st != "ok" || !*ran {
			t.Fatalf("a lock of a dead owner must be taken over: status=%q out=%q", st, out)
		}
		if pidDuring != strconv.Itoa(os.Getpid()) {
			t.Fatalf("lock must carry our pid while held, got %q", pidDuring)
		}
		if _, err := os.Stat(o.SharedLockDir); !os.IsNotExist(err) {
			t.Fatal("lock not released")
		}
	})
	t.Run("no pid, fresh", func(t *testing.T) {
		o, ran := newRunner(t)
		lockWithPid(t, o.SharedLockDir, 0)
		st, out, _ := o.SmartUpgrade(context.Background())
		if st != "locked" || *ran || strings.Contains(out, "идёт обновление") || !strings.Contains(out, "неизвест") {
			t.Fatalf("status=%q ran=%v out=%q", st, *ran, out)
		}
	})
}

// Cron-скрипт тоже пишет свой pid в замок и снимает замок вместе с ним.
func TestOpkgCronScriptWritesPidIntoLock(t *testing.T) {
	s := (&OpkgCronManager{}).scriptText()
	for _, want := range []string{`echo $$ > "$LOCK/pid"`, `rm -f "$LOCK/pid"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("cron script missing %q", want)
		}
	}
}
