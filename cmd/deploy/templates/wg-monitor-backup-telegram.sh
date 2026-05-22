#!/bin/sh
set -eu

umask 077

STATE_DIR=${WGMON_BACKUP_STATE_DIR:-/var/lib/wg-monitor}
BACKUP_DIR=${WGMON_BACKUP_DIR:-$STATE_DIR/backups}
DB_PATH=${WGMON_DB_PATH:-/var/lib/wg-monitor/state.db}
CONFIG_PATH=${WGMON_CONFIG_PATH:-/etc/wg-monitor/backend.yaml}
TOKEN_FILE=${WGMON_BOT_TOKEN_FILE:-/etc/wg-monitor/bot-token.txt}
LIMIT_BYTES=${WGMON_TG_BACKUP_LIMIT_BYTES:-47185920}

mkdir -p "$BACKUP_DIR"

stamp=$(date -u +%Y%m%dT%H%M%SZ)
tmpdir=$(mktemp -d "$STATE_DIR/backup-tmp.XXXXXX")
archive="$BACKUP_DIR/wg-monitor-backup-$stamp.tgz"

cleanup() {
    rm -rf "$tmpdir"
}
trap cleanup EXIT INT TERM

send_message() {
    text=$1
    token=$(tr -d '\r\n' < "$TOKEN_FILE")
    curl -fsS --max-time 30 -X POST "https://api.telegram.org/bot${token}/sendMessage" \
        -d chat_id="$chat_id" \
        --data-urlencode text="$text" >/dev/null || true
}

db_copy="$tmpdir/state.db"
db_copy_sql=$(printf "%s" "$db_copy" | sed "s/'/''/g")
sqlite3 "$DB_PATH" "VACUUM INTO '$db_copy_sql'"

cp "$CONFIG_PATH" "$tmpdir/backend.yaml"

chat_id=$(awk -F: '/^[[:space:]]*admin_user_id:/ {gsub(/[[:space:]]/, "", $2); print $2; exit}' "$CONFIG_PATH")
if [ -z "${chat_id:-}" ] || [ "$chat_id" = "0" ]; then
    chat_id=$(awk -F: '/^[[:space:]]*chat_id:/ {gsub(/[[:space:]]/, "", $2); print $2; exit}' "$CONFIG_PATH")
fi
if [ -z "${chat_id:-}" ] || [ "$chat_id" = "0" ]; then
    echo "telegram chat_id/admin_user_id is not configured" >&2
    exit 1
fi

sqlite3 -header -csv "$db_copy" \
    "SELECT nickname, kind, COALESCE(last_seen_at, '') AS last_seen_at, COALESCE(last_deployed_version, '') AS last_deployed_version FROM users ORDER BY nickname;" \
    > "$tmpdir/agents.csv" 2>/dev/null || true

backend_version=$(/usr/local/bin/wg-monitor-backend -version 2>/dev/null || true)
cat > "$tmpdir/manifest.txt" <<EOF
name=wg-monitor-backup
created_utc=$stamp
backend_version=$backend_version
db_path=$DB_PATH
config_path=$CONFIG_PATH
host=$(hostname 2>/dev/null || true)
EOF

tar -C "$tmpdir" -czf "$archive" state.db backend.yaml agents.csv manifest.txt
size=$(wc -c < "$archive" | tr -d ' ')

if [ "$size" -gt "$LIMIT_BYTES" ]; then
    send_message "wg-monitor backup too large: $archive is $size bytes; left on VPS"
    find "$BACKUP_DIR" -maxdepth 1 -name 'wg-monitor-backup-*.tgz' -mtime +7 -delete
    exit 0
fi

token=$(tr -d '\r\n' < "$TOKEN_FILE")
curl -fsS --max-time 120 -X POST "https://api.telegram.org/bot${token}/sendDocument" \
    -F chat_id="$chat_id" \
    -F document=@"$archive" \
    -F caption="wg-monitor backup $stamp ($size bytes)" >/dev/null

find "$BACKUP_DIR" -maxdepth 1 -name 'wg-monitor-backup-*.tgz' -mtime +7 -delete
