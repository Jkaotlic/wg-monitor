package awgmrelay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryPreservesFilesAndHoldsBeforeLinks(t *testing.T) {
	for _, tc := range []string{"missing", "existing", "dangling", "dangling-wget", "live-lock", "unknown-lock", "stale-lock", "extra-lock-file", "agent-live-lock", "agent-unknown-lock", "agent-stale-lock", "symlink-lock", "cleanup-failure"} {
		t.Run(tc, func(t *testing.T) {
			root := t.TempDir()
			opt := filepath.Join(root, "opt")
			tmp := filepath.Join(root, "tmp")
			proc := filepath.Join(root, "proc")
			for _, p := range []string{opt + "/bin", opt + "/var/wg-monitor", tmp, proc} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			bb := `#!/bin/sh
app=$(/usr/bin/basename "$0")
if [ "$app" = busybox ]; then app=$1; shift; fi
case "$app" in
 --list) printf '%s\n' df sh ash rm sleep cp mv chmod grep ps kill ln mkdir rmdir pidof cat ls wget; exit 0;;
 pidof) exit 1;;
 ash) app=sh;;
esac
exec "$app" "$@"
`
			mustWrite := func(p, s string) {
				t.Helper()
				if err := os.WriteFile(p, []byte(s), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "cleanup-failure" {
				bb = strings.Replace(bb, `exec "$app" "$@"`, `if [ "$app" = rm ]; then case "$*" in *wg-monitor-opkg-auto-upgrade.lock/pid*) exit 1;; esac; fi
exec "$app" "$@"`, 1)
			}
			mustWrite(opt+"/bin/busybox", bb)
			status := opt + "/status"
			mustWrite(status, "Package: busybox\nVersion: 1.37.0-6\nStatus: install user installed\n\n")
			mustWrite(opt+"/bin/opkg", "#!/bin/sh\ncase \"$1\" in\nflag) printf 'Package: busybox\\nVersion: 1.37.0-6\\nStatus: install hold installed\\n\\n' > '"+status+"';;\nstatus) cat '"+status+"';;\n*) exit 2;;\nesac\n")
			if tc == "existing" {
				mustWrite(opt+"/bin/grep", "#!/bin/sh\nexit 0\n")
			}
			if tc == "dangling" {
				if err := os.Symlink("/nonexistent-preserved-target", opt+"/bin/grep"); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "dangling-wget" {
				if err := os.Symlink("/nonexistent-preserved-target", opt+"/bin/wget"); err != nil {
					t.Fatal(err)
				}
			}
			lock := tmp + "/wg-monitor-opkg-auto-upgrade.lock"
			if strings.Contains(tc, "lock") {
				os.Mkdir(lock, 0700)
				mustWrite(lock+"/pid", "321\n")
			}
			if tc == "live-lock" {
				os.Mkdir(proc+"/321", 0700)
			}
			if tc == "unknown-lock" {
				mustWrite(lock+"/pid", "unknown\n")
			}
			if strings.HasPrefix(tc, "agent-") {
				owner := "pid=456\n"
				if tc == "agent-unknown-lock" {
					owner = "invalid\n"
				}
				mustWrite(opt+"/var/wg-monitor/opkg.lock", owner)
				if tc == "agent-live-lock" {
					os.Mkdir(proc+"/456", 0700)
				}
			}
			if tc == "symlink-lock" {
				os.RemoveAll(lock)
				foreign := filepath.Join(root, "foreign")
				os.Mkdir(foreign, 0700)
				mustWrite(foreign+"/pid", "321\n")
				os.Symlink(foreign, lock)
			}
			if tc == "extra-lock-file" {
				mustWrite(lock+"/unrelated", "preserve")
			}
			body := strings.NewReplacer("/opt", opt, "/tmp", tmp, "/proc", proc).Replace(string(EntwareRecoveryScript))
			script := filepath.Join(root, "recover.run")
			mustWrite(script, body)
			out, err := exec.Command("/bin/sh", script).CombinedOutput()
			if tc == "cleanup-failure" {
				if err == nil || strings.Contains(string(out), "RECOVERY_CONFIRMED") {
					t.Fatalf("cleanup false success: %v %s", err, out)
				}
				return
			}
			refused := tc == "live-lock" || tc == "unknown-lock" || tc == "extra-lock-file" || tc == "agent-live-lock" || tc == "agent-unknown-lock" || tc == "symlink-lock"
			if refused {
				if err == nil {
					t.Fatalf("unsafe lock accepted: %s", out)
				}
				st, _ := os.ReadFile(status)
				if strings.Contains(string(st), "hold") {
					t.Fatal("modified package despite refused lock")
				}
				return
			}
			if err != nil || !strings.Contains(string(out), "RECOVERY_CONFIRMED") {
				t.Fatalf("recovery failed: %v %s", err, out)
			}
			st, _ := os.ReadFile(status)
			if !strings.Contains(string(st), "install hold installed") {
				t.Fatal("hold absent")
			}
			if tc == "dangling" {
				target, e := os.Readlink(opt + "/bin/grep")
				if e != nil || target != "/nonexistent-preserved-target" || !strings.Contains(string(out), "PRESERVED_BROKEN_APPLET=grep") {
					t.Fatalf("dangling replaced/hidden: %s %v %s", target, e, out)
				}
			}
			if tc == "existing" {
				st, _ := os.ReadFile(opt + "/bin/grep")
				if string(st) != "#!/bin/sh\nexit 0\n" {
					t.Fatal("existing command overwritten")
				}
			}
			if tc == "dangling-wget" && !strings.Contains(string(out), "PRESERVED_BROKEN_APPLET=wget") {
				t.Fatalf("warning hidden: %s", out)
			}
			journals, _ := filepath.Glob(tmp + "/wgm-busybox-recover-*.links")
			if len(journals) != 0 {
				t.Fatal("successful recovery left journal")
			}
			for _, name := range []string{"df", "sh", "rm", "sleep"} {
				target, e := os.Readlink(opt + "/bin/" + name)
				if e != nil || target != opt+"/bin/busybox" {
					t.Fatalf("missing link %s: %s %v", name, target, e)
				}
			}
			if _, e := os.Stat(lock); !os.IsNotExist(e) {
				t.Fatalf("own lock remains: %v", e)
			}
		})
	}
}
