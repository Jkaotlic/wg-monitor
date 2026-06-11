# Entware Cleanup Dashboard Action

Date: 2026-06-11

The dashboard can deploy and manage a small router-side Entware cleanup script
through the normal agent command channel.

## Commands

- `entware_clean_status` checks install state, cron schedule, `/opt` free space,
  memory snapshot, and log tail.
- `entware_clean_install` writes the managed script and root crontab block.
  Default schedule is `05:15`.
- `entware_clean_run` runs the installed script immediately.
- `entware_clean_logs` returns a larger log tail.
- `entware_clean_remove` removes only the managed crontab block and managed
  script file.

## Managed Paths

- Script: `/opt/etc/wg-monitor/entware-cleanup.sh`
- Log: `/opt/var/log/wg-monitor/entware-cleanup.log`
- Cron block markers:
  - `# wg-monitor entware cleanup begin`
  - `# wg-monitor entware cleanup end`

## Safety Rails

The cleanup script is intentionally narrow:

- checks `/opt` free space before doing any work;
- cleans only old temp/cache contents under `/opt/tmp`, `/opt/var/tmp`,
  `/opt/var/cache/opkg`, and `/opt/var/opkg-lists`;
- does not remove installed packages, opkg database, configs, keys, tokens, or
  service files;
- does not restart services or kill processes;
- records `MemAvailable` before and after cleanup;
- runs `sync`;
- drops Linux page caches only when `/proc/sys/vm/drop_caches` is writable and
  `MemAvailable` is below the built-in threshold;
- trims its own log to a bounded size.

## Dashboard Flow

Open an agent drawer and use the `Entware cleanup` section:

1. `Check` to read current state.
2. Pick `Run time`.
3. `Install schedule` to deploy the script and cron block.
4. `Run now` for a manual cleanup.
5. `Logs` to inspect the last cleanup output.
6. `Remove` to uninstall the managed script and cron block.
