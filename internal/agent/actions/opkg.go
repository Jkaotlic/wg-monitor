package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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
	LockPath   string
	LockTTL    time.Duration
	Exec       ExecFunc
	Now        func() time.Time
	// ConfigRoot is the absolute directory containing `opkg.conf` and the `opkg/`
	// subdirectory of per-feed `.conf` files. Defaults to "/opt/etc" when
	// empty — tests substitute t.TempDir() to point at a sandbox.
	ConfigRoot string
}

// configRoot returns ConfigRoot or the production default.
func (o *OpkgRunner) configRoot() string {
	if o.ConfigRoot != "" {
		return o.ConfigRoot
	}
	return "/opt/etc"
}

// opkgConfPaths returns the candidate paths that may declare feeds, in
// scan order: the single-file `opkg.conf` first, then any per-feed
// `opkg/*.conf`. Missing files are silently skipped by callers.
func (o *OpkgRunner) opkgConfPaths() []string {
	root := o.configRoot()
	// Glob error is ignored on purpose: the pattern is a compile-time literal,
	// so only ErrBadPattern is possible and the pattern is well-formed.
	matches, _ := filepath.Glob(filepath.Join(root, "opkg", "*.conf"))
	paths := []string{filepath.Join(root, "opkg.conf")}
	return append(paths, matches...)
}

func (o *OpkgRunner) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// SmartUpgrade is the live opkg update + upgrade flow with a /opt
// space safety check. It:
//  1. takes the lock-file (refuses if a fresh one exists),
//  2. runs `opkg update` — tolerates partial feed failure as long as at
//     least one feed downloaded successfully (common when a GitHub Pages
//     feed goes 404; blocking the whole upgrade for one dead feed is
//     worse than surfacing the failure and continuing),
//  3. lists upgradable pkgs and estimates installed-size from `opkg info`,
//  4. checks df -k /opt — refuses if the post-upgrade free space would
//     drop below 10% of the partition (Entware on Keenetic typically has
//     150-250 MB free, so we want a meaningful headroom),
//  5. runs `opkg upgrade` if all checks pass.
//
// Returns wire-protocol status ∈ {"ok","err","locked"}, a multi-line
// human report, and a wire.OpkgUpgradeResult payload (FailedFeeds lists
// any feeds that returned HTTP errors during `opkg update`). Designed to
// be safe-by-default: any size estimation error is treated as "skip the
// package in the estimate", so the headroom check errs on the side of
// *under-estimating* the install need (the caller's 10% margin absorbs
// the slack).
func (o *OpkgRunner) SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult) {
	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held), payload
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error(), payload
	}
	defer o.releaseLock()

	updateOut, updateErr := o.Exec(ctx, "opkg", "update")
	upd := parseOpkgUpdate(string(updateOut))
	payload.FailedFeeds = upd.failedFeeds
	if updateErr != nil && upd.feedsUpdated == 0 {
		// Total failure: no feed downloaded successfully — can't proceed.
		return "err", "opkg update failed: " + updateErr.Error() + "\n" + string(updateOut), payload
	}
	// Partial success: at least one feed updated. Continue with the upgrade
	// flow; failed-feeds list is surfaced in the final report and in payload.

	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "opkg list-upgradable failed: " + err.Error() + "\n" + string(listing), payload
	}
	pkgs := parseUpgradablePkgs(string(listing))
	listingHasNoise := strings.Contains(string(listing), "ERROR") || strings.Contains(string(listing), "WARNING")
	if len(pkgs) == 0 && !listingHasNoise {
		out := "✅ Все пакеты актуальны — обновлять нечего."
		out = appendFailedFeedsBlock(out, upd.failedFeeds)
		payload.Output = out
		return "ok", out, payload
	}

	freeKB, totalKB, err := o.dfOpt(ctx)
	if err != nil {
		return "err", "df /opt failed: " + err.Error(), payload
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
			), payload
		}
	}

	upgradeOut, err := o.Exec(ctx, "opkg", "upgrade")
	if err != nil {
		return "err", "opkg upgrade failed: " + err.Error() + "\n" + string(upgradeOut), payload
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
	out := fmt.Sprintf(
		"✅ Обновлено пакетов: %d (%s)\n"+
			"Список: %s\n"+
			"Свободно после: %s / %s\n\n"+
			"%s",
		len(pkgs), sizeNote, pkgListStr,
		humanKB(post), humanKB(totalKB),
		strings.TrimSpace(string(upgradeOut)),
	)
	out = appendFailedFeedsBlock(out, upd.failedFeeds)
	payload.Output = out
	return "ok", out, payload
}

// appendFailedFeedsBlock appends a "⚠️ Недоступные фиды" section to a
// SmartUpgrade report when at least one feed failed to download during
// `opkg update`. No-op when the list is empty.
func appendFailedFeedsBlock(report string, failed []string) string {
	if len(failed) == 0 {
		return report
	}
	var b strings.Builder
	b.WriteString(report)
	b.WriteString("\n\n⚠️ Недоступные фиды:")
	for _, u := range failed {
		b.WriteString("\n • ")
		b.WriteString(u)
	}
	return b.String()
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

// normalizeFeedURL strips a trailing `/Packages.gz` (once) and any trailing
// `/`, returning the base URL form that appears in opkg config files
// (`src/gz <name> <base-url>`).
func normalizeFeedURL(u string) string {
	u = strings.TrimSuffix(u, "/Packages.gz")
	u = strings.TrimRight(u, "/")
	return u
}

// disableMatchingLine scans `body` line-by-line, commenting out any
// uncommented `src` or `src/gz` line whose URL (third whitespace-separated
// field) equals normalizedURL. Returns the rewritten body and true iff at
// least one line was modified. Already-commented lines (leading `#`) are
// never re-touched.
//
// stamp is the timestamp string baked into the comment prefix; the caller
// owns time-source choice for deterministic tests.
func disableMatchingLine(body []byte, normalizedURL, stamp string) ([]byte, bool) {
	lines := strings.SplitAfter(string(body), "\n")
	hit := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		if fields[0] != "src" && fields[0] != "src/gz" {
			continue
		}
		lineURL := strings.TrimRight(fields[2], "/")
		if lineURL != normalizedURL {
			continue
		}
		// Preserve the original line in the suffix so reverting is just
		// `sed -i 's|^# disabled by wg-monitor [^:]*: ||'`.
		lines[i] = "# disabled by wg-monitor " + stamp + ": " + line
		hit = true
	}
	if !hit {
		return body, false
	}
	return []byte(strings.Join(lines, "")), true
}

// DisableFeed comments out the line in opkg config matching the given feed
// URL, writes a timestamped backup of the modified file, then re-runs
// SmartUpgrade so the user sees a single clean report.
//
// Behavior table:
//   - Invalid URL (not http/https): status="err", no FS access.
//   - URL not present in any conf: status="ok", "уже отключён или не найден", no FS writes.
//   - URL matched and commented: status from SmartUpgrade re-run (typically "ok").
//   - Lock held by concurrent op: status="locked".
//
// The opkg lock-file is held during the disable phase, released before the
// SmartUpgrade re-run (which takes its own lock). This means a concurrent
// SmartUpgrade can squeeze in between, which is acceptable: at worst the
// user sees two upgrade reports in quick succession.
func (o *OpkgRunner) DisableFeed(ctx context.Context, rawURL string) (status, output string, payload wire.OpkgUpgradeResult) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "err", "invalid feed URL: " + rawURL, payload
	}
	url := normalizeFeedURL(rawURL)

	if held, age, ok := o.lockHeldFresh(); ok {
		return "locked", fmt.Sprintf("opkg lock held by another op (age %v, lock file: %s)", age.Round(time.Second), held), payload
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error(), payload
	}

	stamp := o.now().UTC().Format(time.RFC3339)
	backupSuffix := o.now().UTC().Format("20060102-150405")

	var changedFiles []string
	for _, path := range o.opkgConfPaths() {
		body, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			o.releaseLock()
			return "err", fmt.Sprintf("read %s: %v", path, err), payload
		}
		newBody, hit := disableMatchingLine(body, url, stamp)
		if !hit {
			continue
		}
		if err := backupAndWrite(path, body, newBody, backupSuffix); err != nil {
			o.releaseLock()
			return "err", fmt.Sprintf("rewrite %s: %v", path, err), payload
		}
		changedFiles = append(changedFiles, path)
	}

	o.releaseLock()

	if len(changedFiles) == 0 {
		return "ok", fmt.Sprintf("🔧 Фид %s уже отключён или не найден в opkg-конфигах.", url), payload
	}

	prefix := fmt.Sprintf("🔧 Отключён фид %s в %s (backup suffix: %s)\n\n",
		url, strings.Join(changedFiles, ", "), backupSuffix)

	s, smartOut, p := o.SmartUpgrade(ctx)
	combined := prefix + smartOut
	p.Output = combined
	return s, combined, p
}

// backupAndWrite writes oldBody to <path>.bak.<suffix>, then atomically
// replaces <path> with newBody. Atomicity via tmp-file + Rename ensures no
// half-written config is ever visible to opkg.
func backupAndWrite(path string, oldBody, newBody []byte, suffix string) error {
	bakPath := path + ".bak." + suffix
	if err := os.WriteFile(bakPath, oldBody, 0o644); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	tmpPath := path + ".tmp." + suffix
	if err := os.WriteFile(tmpPath, newBody, 0o644); err != nil {
		return fmt.Errorf("tmp write: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
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
