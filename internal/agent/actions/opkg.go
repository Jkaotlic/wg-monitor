package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ExecFunc is the contract for invoking external programs from OpkgRunner.
// Returns combined-output bytes plus an error (matching exec.Cmd.CombinedOutput).
// Injectable so tests don't need a real opkg binary.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultExec uses os/exec with combined stderr+stdout capture.
func DefaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// OpkgRunner enforces a lock-file so two opkg actions don't overlap and
// performs the dry-run preflight (`opkg update` + `opkg list-upgradable`).
//
// The actual `opkg upgrade` step is intentionally NOT executed yet — that's
// deferred to a later iteration where the TG flow includes a confirm step.
// Today's purpose: surface "what would change" to the admin in TG without
// touching the system.
type OpkgRunner struct {
	LockPath string
	LockTTL  time.Duration
	Exec     ExecFunc
	Now      func() time.Time
}

func (o *OpkgRunner) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// SmartUpgrade is the live opkg update + upgrade flow with a /opt
// space safety check. It:
//   1. takes the lock-file (refuses if a fresh one exists),
//   2. runs `opkg update`,
//   3. lists upgradable pkgs and estimates installed-size from `opkg info`,
//   4. checks df -k /opt — refuses if the post-upgrade free space would
//      drop below 10% of the partition (Entware on Keenetic typically has
//      150-250 MB free, so we want a meaningful headroom),
//   5. runs `opkg upgrade` if all checks pass.
//
// Returns wire-protocol status ∈ {"ok","err","locked"} plus a multi-line
// human report. Designed to be safe-by-default: any size estimation error
// is treated as "skip the package in the estimate", so the headroom check
// errs on the side of *under-estimating* the install need (the caller's
// 10% margin absorbs the slack).
func (o *OpkgRunner) SmartUpgrade(ctx context.Context) (status, output string) {
	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held)
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error()
	}
	defer o.releaseLock()

	if out, err := o.Exec(ctx, "opkg", "update"); err != nil {
		return "err", "opkg update failed: " + err.Error() + "\n" + string(out)
	}

	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "opkg list-upgradable failed: " + err.Error() + "\n" + string(listing)
	}
	pkgs := parseUpgradablePkgs(string(listing))
	listingHasNoise := strings.Contains(string(listing), "ERROR") || strings.Contains(string(listing), "WARNING")
	if len(pkgs) == 0 && !listingHasNoise {
		return "ok", "✅ Все пакеты актуальны — обновлять нечего."
	}

	freeKB, totalKB, err := o.dfOpt(ctx)
	if err != nil {
		return "err", "df /opt failed: " + err.Error()
	}
	// Space safety check is meaningful only when we have a parsed pkg list.
	// If list-upgradable returned only noise (ERROR/WARNING but `opkg upgrade`
	// may still find work via package feeds), we proceed without a preflight
	// estimate — `opkg upgrade` itself will refuse if a single download would
	// fill /opt mid-flight.
	neededKB := int64(0)
	if len(pkgs) > 0 {
		neededKB = o.estimateInstallSizeKB(ctx, pkgs)
		headroomKB := totalKB / 10 // require ≥ 10% free post-upgrade
		if freeKB-neededKB < headroomKB {
			return "err", fmt.Sprintf(
				"❌ Не хватит места на /opt.\n"+
					"Пакетов к обновлению: %d\n"+
					"Оценка установки: %s\n"+
					"Свободно сейчас: %s / всего %s\n"+
					"После upgrade осталось бы: %s (порог headroom %s = 10%%)\n"+
					"Освободи /opt и повтори.",
				len(pkgs), humanKB(neededKB), humanKB(freeKB), humanKB(totalKB),
				humanKB(freeKB-neededKB), humanKB(headroomKB),
			)
		}
	}

	upgradeOut, err := o.Exec(ctx, "opkg", "upgrade")
	if err != nil {
		return "err", "opkg upgrade failed: " + err.Error() + "\n" + string(upgradeOut)
	}
	// Recover the actual upgraded package names from `opkg upgrade` stdout.
	// list-upgradable can lie (multi-feed conflicts surface as ERROR with
	// no rows), but the upgrade command's "Upgrading <pkg> on root..." lines
	// are authoritative — they print only for packages that actually moved.
	if upgraded := parseUpgradedFromOutput(string(upgradeOut)); len(upgraded) > 0 {
		pkgs = upgraded
	}
	post, _, _ := o.dfOpt(ctx) // post-upgrade free; ignore err — best-effort

	sizeNote := "~" + humanKB(neededKB)
	if neededKB == 0 {
		sizeNote = "размер не определён"
	}
	pkgListStr := strings.Join(pkgs, ", ")
	if pkgListStr == "" {
		pkgListStr = "(не определён)"
	}
	if len(pkgListStr) > 200 {
		pkgListStr = pkgListStr[:200] + "…"
	}
	return "ok", fmt.Sprintf(
		"✅ Обновлено пакетов: %d (%s)\n"+
			"Список: %s\n"+
			"Свободно после: %s / %s\n\n"+
			"%s",
		len(pkgs), sizeNote, pkgListStr,
		humanKB(post), humanKB(totalKB),
		strings.TrimSpace(string(upgradeOut)),
	)
}

// dfOpt parses busybox `df -k /opt` and returns (free, total) in KB.
// Falls back gracefully on unexpected output formats.
func (o *OpkgRunner) dfOpt(ctx context.Context) (free, total int64, err error) {
	out, err := o.Exec(ctx, "df", "-k", "/opt")
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output: %s", string(out))
	}
	// Busybox df: "Filesystem 1024-blocks Used Available Capacity Mounted on"
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("unexpected df fields: %q", lines[1])
	}
	total, _ = strconv.ParseInt(fields[1], 10, 64)
	free, _ = strconv.ParseInt(fields[3], 10, 64)
	return free, total, nil
}

// estimateInstallSizeKB sums Installed-Size: from `opkg info <pkg>` for
// every upgradable package. Best-effort — any per-pkg failure is silently
// skipped (the 10% headroom margin in SmartUpgrade absorbs underestimates).
func (o *OpkgRunner) estimateInstallSizeKB(ctx context.Context, pkgs []string) int64 {
	var totalKB int64
	for _, p := range pkgs {
		out, err := o.Exec(ctx, "opkg", "info", p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			v, ok := strings.CutPrefix(line, "Installed-Size:")
			if !ok {
				continue
			}
			bytes, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			totalKB += bytes / 1024
			break
		}
	}
	return totalKB
}

// parseUpgradablePkgs extracts package names from `opkg list-upgradable`
// output. Strict format: each line must split as "name - vOld - vNew" via
// the literal " - " separator. Anything else (ERROR:/WARNING:/multi-feed
// notices that opkg sometimes prints into the same stream) is dropped.
func parseUpgradablePkgs(listing string) []string {
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " - ", 3)
		if len(parts) != 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		// First-token sanity: package names are lowercase + digits/-/_/+/.
		if name == "" || !looksLikePkgName(name) {
			continue
		}
		pkgs = append(pkgs, name)
	}
	return pkgs
}

// parseUpgradedFromOutput extracts package names from `opkg upgrade` stdout
// — used as a source of truth when list-upgradable was noisy. Lines look
// like "Upgrading hrweb on root from 1.24.0-1 to 1.26.0-1..." (one per
// actually-upgraded package).
func parseUpgradedFromOutput(out string) []string {
	var pkgs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Upgrading ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if !seen[name] && looksLikePkgName(name) {
			seen[name] = true
			pkgs = append(pkgs, name)
		}
	}
	return pkgs
}

// looksLikePkgName accepts lowercase letters, digits, and the punctuation
// opkg permits in package names (`-`, `_`, `+`, `.`).
func looksLikePkgName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '+' || r == '.':
		default:
			return false
		}
	}
	return true
}

// opkgUpdateOutcome is the parsed shape of `opkg update` combined output.
type opkgUpdateOutcome struct {
	feedsUpdated int      // count of "Updated list of available packages in ..." lines
	failedFeeds  []string // URLs from "*** Failed to download the package list from <url>" lines
}

// parseOpkgUpdate scans opkg's combined output and tallies feed success/failure.
// `Collected errors:` block is ignored — URLs appear there in a different
// format and would otherwise double-count.
func parseOpkgUpdate(out string) opkgUpdateOutcome {
	var o opkgUpdateOutcome
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Updated list of available packages in "):
			o.feedsUpdated++
		case strings.HasPrefix(line, "*** Failed to download the package list from "):
			url := strings.TrimPrefix(line, "*** Failed to download the package list from ")
			url = strings.TrimSpace(url)
			if url != "" {
				o.failedFeeds = append(o.failedFeeds, url)
			}
		}
	}
	return o
}

func humanKB(kb int64) string {
	if kb < 1024 {
		return fmt.Sprintf("%d KB", kb)
	}
	mb := float64(kb) / 1024.0
	return fmt.Sprintf("%.1f MB", mb)
}

// DryRun returns ("locked", ...) if a fresh lock-file exists, otherwise
// runs the preflight, releases the lock, and returns ("ok", listing) or
// ("err", explanation).
//
// DEPRECATED 2026-05-06: kept for the OpkgExecutor interface contract +
// any external callers (none known in the tree). New work calls
// SmartUpgrade — it does the live upgrade with a space safety check.
func (o *OpkgRunner) DryRun(ctx context.Context) (status, output string) {
	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held)
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error()
	}
	defer o.releaseLock()

	if out, err := o.Exec(ctx, "opkg", "update"); err != nil {
		return "err", "opkg update failed: " + err.Error() + "\n" + string(out)
	}
	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "opkg list-upgradable failed: " + err.Error() + "\n" + string(listing)
	}
	if strings.TrimSpace(string(listing)) == "" {
		return "ok", "all packages up to date"
	}
	return "ok", "upgradable packages:\n" + string(listing)
}

func (o *OpkgRunner) lockHeldFresh() (path string, age time.Duration, ok bool) {
	st, err := os.Stat(o.LockPath)
	if err != nil {
		return "", 0, false
	}
	age = o.now().Sub(st.ModTime())
	if age < o.LockTTL {
		return o.LockPath, age, true
	}
	return "", age, false
}

func (o *OpkgRunner) takeLock() error {
	if err := os.MkdirAll(parentDir(o.LockPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(o.LockPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if cerr := f.Close(); cerr != nil {
		return cerr
	}
	// Touch mtime to "now" — covers the case where the file existed but was stale.
	now := o.now()
	if err := os.Chtimes(o.LockPath, now, now); err != nil {
		return err
	}
	return nil
}

func (o *OpkgRunner) releaseLock() {
	if err := os.Remove(o.LockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// best-effort: nothing meaningful to do; next call will see stale-lock.
	}
}

// parentDir returns filepath.Dir without importing path/filepath at the top
// (keep imports tight). For lock paths like "/opt/var/wg-monitor/opkg.lock"
// this yields "/opt/var/wg-monitor".
func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
