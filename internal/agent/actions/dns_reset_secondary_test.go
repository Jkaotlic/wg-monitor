package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestDNSAddSecondaryPreservesConfiguration(t *testing.T) {
	const before = "dns-proxy\n    tls upstream 1.1.1.1 sni cloudflare-dns.com\n    tls upstream common.dot.dns.yandex.net domain ru\n    https upstream https://private.example/secret\n!\nip name-server 8.8.8.8 on Wireguard0\n"
	const secondary = "    tls upstream 1.0.0.1:853 sni cloudflare-dns.com\n"
	for _, tc := range []struct {
		name                                                                      string
		dry, present, probeFail, dropOld, noApply, badSNI, saveFail, snapshotFail bool
		want                                                                      string
	}{
		{name: "apply", want: "ok"},
		{name: "preview", dry: true, want: "ok"},
		{name: "idempotent", present: true, want: "ok"},
		{name: "probe failure", probeFail: true, want: "err"},
		{name: "existing entry lost", dropOld: true, want: "partial"},
		{name: "apply silently ignored", noApply: true, want: "partial"},
		{name: "wrong SNI not confirmed", badSNI: true, want: "partial"},
		{name: "save fails", saveFail: true, want: "partial"},
		{name: "backup fails", snapshotFail: true, want: "err"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := before
			after := strings.Replace(before, "!\nip name-server", secondary+"!\nip name-server", 1)
			if tc.present {
				config = after
			}
			if tc.dropOld {
				after = "dns-proxy\n" + secondary + "!\n"
			}
			if tc.noApply {
				after = before
			}
			if tc.badSNI {
				after = strings.Replace(after, "1.0.0.1:853 sni cloudflare-dns.com", "1.0.0.1:853 sni wrong.example", 1)
			}
			if tc.snapshotFail {
				if err := os.WriteFile(filepath.Join(dir, "blocked"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(dir, "blocked", "child")
			}
			f := &fakeDNSExec{runningConfig: config, afterConfig: after, failOn: map[string]bool{"system configuration save": tc.saveFail}}
			probes := 0
			execWithBackupCheck := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if len(args) == 2 && args[1] == "dns-proxy tls upstream 1.0.0.1 sni cloudflare-dns.com" {
					files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
					if len(files) != 1 {
						t.Fatal("mutation attempted before backup")
					}
				}
				return f.exec(ctx, name, args...)
			}
			r := &Runner{Exec: execWithBackupCheck, ConfigPath: filepath.Join(dir, "config.yaml"), DNSProbe: func(_ context.Context, line, domain string) error {
				probes++
				if line != "tls upstream 1.0.0.1 sni cloudflare-dns.com" || domain == "" {
					t.Errorf("unexpected probe %q %q", line, domain)
				}
				if tc.probeFail {
					return errors.New("unreachable")
				}
				return nil
			}}
			res := r.Execute(context.Background(), wire.Command{ID: "secondary", Action: "dns_reset", Args: map[string]any{"mode": "add_secondary", "dry_run": tc.dry}})
			if res.Status != tc.want {
				t.Fatalf("status %s want %s: %s", res.Status, tc.want, res.Output)
			}
			var want []string
			if !tc.dry && !tc.probeFail && !tc.snapshotFail {
				if !tc.present {
					want = append(want, "dns-proxy tls upstream 1.0.0.1 sni cloudflare-dns.com")
				}
				if !tc.dropOld && !tc.noApply && !tc.badSNI {
					want = append(want, "system configuration save")
				}
			}
			if !reflect.DeepEqual(f.calls, want) {
				t.Errorf("mutations %v want %v", f.calls, want)
			}
			if tc.dry {
				files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
				if len(files) != 0 {
					t.Error("preview wrote snapshot")
				}
			}
			if probes != 1 {
				t.Errorf("candidate probes %d", probes)
			}
			if !tc.dry && !tc.probeFail && !tc.snapshotFail {
				files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
				if len(files) != 1 {
					t.Fatalf("snapshots %v", files)
				}
				b, err := os.ReadFile(files[0])
				if err != nil || string(b) != config {
					t.Errorf("backup not exact: %v", err)
				}
			}
			if strings.Contains(res.Output, "/secret") {
				t.Error("private DoH path leaked")
			}
		})
	}
}

func TestDNSResetRejectsUnknownMode(t *testing.T) {
	f := &fakeDNSExec{runningConfig: sampleRunningConfig}
	r := &Runner{Exec: f.exec}
	res := r.Execute(context.Background(), wire.Command{ID: "invalid", Action: "dns_reset", Args: map[string]any{"mode": "typo"}})
	if res.Status != "err" || len(f.calls) != 0 {
		t.Fatalf("unknown mode mutated config: %s %v", res.Status, f.calls)
	}
}

func TestDNSAddSecondaryRefusesUnsafePrerequisites(t *testing.T) {
	for _, tc := range []struct {
		name, config        string
		noProbe, noSnapshot bool
		readFail            bool
	}{
		{name: "missing probe", config: sampleRunningConfig, noProbe: true},
		{name: "missing snapshot", config: sampleRunningConfig, noSnapshot: true},
		{name: "read failure", config: sampleRunningConfig, readFail: true},
		{name: "conflicting secondary", config: "dns-proxy\n tls upstream 1.0.0.1 sni other.example domain ru\n!\n"},
		{name: "DoT limit", config: "dns-proxy\n" + strings.Repeat(" tls upstream 9.9.9.9 domain ru\n", 8) + "!\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDNSExec{runningConfig: tc.config}
			if tc.readFail {
				f.rcErr = errors.New("read failed")
			}
			r := &Runner{Exec: f.exec, ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), DNSProbe: func(context.Context, string, string) error { return nil }}
			if tc.noProbe {
				r.DNSProbe = nil
			}
			if tc.noSnapshot {
				r.ConfigPath = ""
			}
			res := r.Execute(context.Background(), wire.Command{ID: "guard", Action: "dns_reset", Args: map[string]any{"mode": "add_secondary"}})
			if res.Status != "err" || len(f.calls) != 0 {
				t.Fatalf("unsafe prerequisites allowed mutation: %s %v %s", res.Status, f.calls, res.Output)
			}
		})
	}
}

// A failed persistence must not turn the next retry into a false success just
// because the new resolver survived in running-config.
func TestDNSAddSecondaryRetriesFailedSaveWithoutReadding(t *testing.T) {
	const before = "dns-proxy\n tls upstream 1.1.1.1 sni cloudflare-dns.com\n!\n"
	const after = "dns-proxy\n tls upstream 1.1.1.1 sni cloudflare-dns.com\n tls upstream 1.0.0.1 sni cloudflare-dns.com\n!\n"
	f := &fakeDNSExec{runningConfig: before, afterConfig: after, failOn: map[string]bool{"system configuration save": true}}
	probes := 0
	r := &Runner{Exec: f.exec, ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), DNSProbe: func(context.Context, string, string) error { probes++; return nil }}
	cmd := wire.Command{ID: "retry", Action: "dns_reset", Args: map[string]any{"mode": "add_secondary"}}
	first := r.Execute(context.Background(), cmd)
	if first.Status != "partial" {
		t.Fatalf("first status %s: %s", first.Status, first.Output)
	}
	f.failOn["system configuration save"] = false
	second := r.Execute(context.Background(), cmd)
	if second.Status != "ok" {
		t.Fatalf("retry status %s: %s", second.Status, second.Output)
	}
	want := []string{"dns-proxy tls upstream 1.0.0.1 sni cloudflare-dns.com", "system configuration save", "system configuration save"}
	if !reflect.DeepEqual(f.calls, want) || probes != 2 {
		t.Fatalf("retry did not verify and persist existing secondary: calls %v probes %d", f.calls, probes)
	}
}
