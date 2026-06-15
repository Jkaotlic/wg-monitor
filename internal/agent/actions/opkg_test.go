package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestOpkg_DryRun_Empty_Ok(t *testing.T) {
	calls := []string{}
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		// `opkg list-upgradable` returns empty body when nothing to upgrade
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return []byte("opkg update OK"), nil
	})
	status, output := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "up to date") {
		t.Errorf("expected 'up to date' summary, got %q", output)
	}
	wantCalls := []string{"opkg update", "opkg list-upgradable"}
	if !equalStrings(calls, wantCalls) {
		t.Errorf("calls: got %v want %v", calls, wantCalls)
	}
}

func TestOpkg_DryRun_NonEmpty_Ok(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "list-upgradable" {
			return []byte("wg-monitor - 0.4.0 - 0.5.0\nlibssl - 3.0.0 - 3.0.1\n"), nil
		}
		return nil, nil
	})
	status, output := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "wg-monitor") || !strings.Contains(output, "libssl") {
		t.Errorf("expected upgrade list in output, got %q", output)
	}
}

func TestOpkg_DryRun_Locked_WhenFreshLock(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("opkg should not be invoked when locked")
		return nil, nil
	})
	// Pre-create a fresh lock file (mtime now)
	if err := os.WriteFile(o.LockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	status, output := o.DryRun(context.Background())
	if status != "locked" {
		t.Errorf("status: %q output: %q", status, output)
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

func TestOpkg_DryRun_StaleLockReleased(t *testing.T) {
	called := 0
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called++
		if args[0] == "list-upgradable" {
			return []byte(""), nil
		}
		return nil, nil
	})
	// Pre-create a STALE lock file (mtime 2 TTL ago)
	if err := os.WriteFile(o.LockPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-1 * time.Second)
	_ = os.Chtimes(o.LockPath, stale, stale)
	status, _ := o.DryRun(context.Background())
	if status != "ok" {
		t.Errorf("expected stale lock to be ignored, got status=%q", status)
	}
	if called == 0 {
		t.Errorf("expected opkg to be invoked despite stale lock")
	}
}

func TestOpkg_DryRun_OpkgUpdateFails_Errs(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "update" {
			return []byte("network down"), errors.New("exit 1")
		}
		return nil, nil
	})
	status, output := o.DryRun(context.Background())
	if status != "err" {
		t.Errorf("status: %q", status)
	}
	if !strings.Contains(output, "exit 1") && !strings.Contains(output, "network down") {
		t.Errorf("output should describe failure, got %q", output)
	}
}

func TestOpkg_DryRun_ReleasesLockOnExit(t *testing.T) {
	o := mkOpkgRunner(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})
	_, _ = o.DryRun(context.Background())
	if _, err := os.Stat(o.LockPath); !os.IsNotExist(err) {
		t.Errorf("lock file should be removed after DryRun, got err=%v", err)
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
