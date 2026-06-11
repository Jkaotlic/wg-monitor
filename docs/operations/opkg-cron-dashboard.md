# OPKG Cron From Dashboard

## What It Does

The dashboard can ask an agent to install a router-side scheduled OPKG updater.
The agent writes a managed shell script and a managed root crontab block. The
backend does not store install truth in SQLite: the dashboard shows the latest
truth returned by the selected router.

## Commands

- `opkg_cron_status`: checks whether the managed script and crontab entry exist,
  reads `/opt` free space, and returns the last log tail.
- `opkg_cron_install`: checks `/opt` free space, writes the script, updates only
  the managed crontab block, and best-effort starts Entware cron.
- `opkg_cron_logs`: returns a larger log tail.
- `opkg_cron_remove`: removes the managed crontab block and script.
- `version_audit`: shows AWG Manager, HR-Neo, and Keenetic firmware versions in
  the dashboard result panel and the selected-agent drawer.

## Router Paths

- Script: `/opt/etc/wg-monitor/opkg-auto-upgrade.sh`
- Log: `/opt/var/log/wg-monitor/opkg-auto-upgrade.log`
- Cron: root crontab, between:
  - `# wg-monitor opkg auto-upgrade begin`
  - `# wg-monitor opkg auto-upgrade end`

Existing crontab lines outside this managed block are preserved.

## Space Guard

The agent checks `df -k /opt` before installing. If free space is below the
configured minimum, install fails and the script is not written.

The generated script repeats the same `/opt` check before every scheduled run.
If space is low, it logs `status=skipped_low_space` and exits without running
`opkg update` or `opkg upgrade`.

Default minimum free space: `10240 KB`.

## Log Cleanup

The generated script trims its log after every run. Default cap: `64 KB`.
This keeps the router from slowly filling `/opt/var/log` with unattended OPKG
output.

## Dashboard Workflow

1. Open an agent in `/dashboard/`.
2. Use `Versions` to refresh AWG Manager, HR-Neo, and firmware versions.
3. In `OPKG cron`, set a time such as `04:30`.
4. Click `Check` to see the current state.
5. Click `Install schedule` to deploy or update the managed schedule.
6. Click `Logs` to inspect the latest scheduled run.
7. Click `Remove` to delete the managed cron job and script.

No RC, release tag, or remote deployment is created by this dashboard feature by
itself.
