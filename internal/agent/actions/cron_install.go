package actions

import (
	"context"
	"fmt"
	"strings"
)

// cronInitScript is the init script the Entware `cron` package drops in. Its
// presence is our proxy for "crond + crontab are installed and runnable".
const cronInitScript = "/opt/etc/init.d/S10cron"

// ensureCronInstalled makes sure the Entware `cron` package (crond +
// /opt/etc/init.d/S10cron) exists before a managed schedule is written.
//
// On a fresh router that package is missing, so editing the root crontab and
// running `S10cron start` both fail silently and the schedule never fires — the
// long-standing "the cron scripts don't install cron themselves" gap. Detection
// and install both go through exec so the agent's per-command timeout applies
// and tests can fake them.
//
// Returns installedNow=true only when this call installed the package (lets the
// caller surface "cron package installed" in the status it reports back).
func ensureCronInstalled(ctx context.Context, exec ExecFunc) (installedNow bool, err error) {
	if exec == nil {
		return false, fmt.Errorf("ensure cron: exec not configured")
	}
	if cronPackagePresent(ctx, exec) {
		return false, nil
	}
	if _, installErr := exec(ctx, "opkg", "install", "cron"); installErr != nil {
		// A fresh Entware install often has no package lists yet, so the first
		// `opkg install` fails with "unknown package". Refresh once and retry.
		if _, updateErr := exec(ctx, "opkg", "update"); updateErr != nil {
			return false, fmt.Errorf("install cron package: opkg install failed (%v) and opkg update failed (%v)", installErr, updateErr)
		}
		if out, retryErr := exec(ctx, "opkg", "install", "cron"); retryErr != nil {
			return false, fmt.Errorf("install cron package: %v\n%s", retryErr, strings.TrimSpace(string(out)))
		}
	}
	if !cronPackagePresent(ctx, exec) {
		return false, fmt.Errorf("install cron package: %s still missing after opkg install cron", cronInitScript)
	}
	return true, nil
}

// cronPackagePresent reports whether the cron init script is installed and
// executable. `test -x` keeps the check to a single, cheap exec.
func cronPackagePresent(ctx context.Context, exec ExecFunc) bool {
	_, err := exec(ctx, "sh", "-c", "test -x "+cronInitScript)
	return err == nil
}
