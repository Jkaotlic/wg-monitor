#!/bin/sh
set -u
PATH=/opt/bin:/opt/sbin:/usr/bin:/usr/sbin:/bin:/sbin
LOG="/opt/var/log/wg-monitor/entware-cleanup.log"
LOCK=/tmp/wg-monitor-entware-cleanup.lock
MIN_FREE_KB=2048
MIN_MEM_AVAILABLE_KB=65536
MAX_LOG_KB=64

mkdir -p "$(dirname "$LOG")"

log() {
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG"
}

trim_log() {
  max_bytes=$((MAX_LOG_KB * 1024))
  [ -f "$LOG" ] || return 0
  size=$(wc -c < "$LOG" 2>/dev/null || echo 0)
  [ "$size" -le "$max_bytes" ] && return 0
  tmp="${LOG}.tmp"
  tail -c "$max_bytes" "$LOG" > "$tmp" 2>/dev/null && mv "$tmp" "$LOG"
}

opt_free_kb() {
  df -k /opt | awk 'NR==2 {print $4}'
}

mem_available_kb() {
  awk '/^MemAvailable:/ {print $2}' /proc/meminfo 2>/dev/null
}

clean_dir_children() {
  dir="$1"
  [ -d "$dir" ] || return 0
  find "$dir" -mindepth 1 -maxdepth 1 -mtime +1 -exec rm -rf -- {} + 2>/dev/null || true
}

clean_old_files() {
  dir="$1"
  [ -d "$dir" ] || return 0
  find "$dir" -type f -mtime +1 -delete 2>/dev/null || true
}

if ! mkdir "$LOCK" 2>/dev/null; then
  log "status=locked another run is active"
  trim_log
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

free="$(opt_free_kb)"
case "$free" in
  ''|*[!0-9]*) log "status=skipped_bad_df free=$free"; trim_log; exit 0 ;;
esac
if [ "$free" -lt "$MIN_FREE_KB" ]; then
  # Мало места -- чистим всё равно: удаление места не требует (AGENT-13).
  log "status=low_space free_kb=$free min_free_kb=$MIN_FREE_KB cleaning anyway"
fi

before="$(mem_available_kb)"
case "$before" in
  ''|*[!0-9]*) before=0 ;;
esac

log "status=start mem_before_kb=$before opt_free_kb=$free"
clean_dir_children /opt/tmp
clean_dir_children /opt/var/tmp
clean_old_files /opt/var/cache/opkg
clean_old_files /opt/var/opkg-lists

sync
if [ -w /proc/sys/vm/drop_caches ] && [ "$before" -gt 0 ] && [ "$before" -lt "$MIN_MEM_AVAILABLE_KB" ]; then
  echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
fi

after="$(mem_available_kb)"
case "$after" in
  ''|*[!0-9]*) after=0 ;;
esac
freed=0
if [ "$before" -gt 0 ] && [ "$after" -gt "$before" ]; then
  freed=$((after - before))
fi
free_after="$(opt_free_kb)"
log "status=ok mem_before_kb=$before mem_after_kb=$after freed_kb=$freed opt_free_before_kb=$free opt_free_kb=$free_after"
trim_log
