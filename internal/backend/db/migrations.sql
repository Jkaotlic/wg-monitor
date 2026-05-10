CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nickname TEXT UNIQUE NOT NULL,
    token_hash TEXT NOT NULL,
    expected_exit_ip TEXT NOT NULL,
    awg_iface TEXT NOT NULL,
    telegram_thread_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_users_token_hash ON users(token_hash);
-- Used by callbacks router on every TG forum-topic message routing
-- (GetByThreadID). Without it the lookup is a full table scan per
-- inbound message.
CREATE INDEX IF NOT EXISTS idx_users_thread_id ON users(telegram_thread_id) WHERE telegram_thread_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    status TEXT NOT NULL,
    details_json TEXT,
    ts TIMESTAMP NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_events_user_ts ON events(user_id, ts DESC);
-- LatestEventsByPrefix and per-user-per-check lookups need check_name in the
-- key — the user_id-only prefix index above can't satisfy them efficiently.
CREATE INDEX IF NOT EXISTS idx_events_user_check_ts ON events(user_id, check_name, ts DESC);
-- retention.PruneBefore and event aggregations across all users.
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);

CREATE TABLE IF NOT EXISTS incident_state (
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    consecutive_fails INTEGER NOT NULL DEFAULT 0,
    consecutive_oks INTEGER NOT NULL DEFAULT 0,
    current_status TEXT NOT NULL DEFAULT 'ok',
    hard_since TIMESTAMP,
    last_alert_msg_id INTEGER,
    last_alert_at TIMESTAMP,
    silenced_until TIMESTAMP,
    acked_until TIMESTAMP,
    PRIMARY KEY (user_id, check_name),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
-- StaleHards / AllActiveHard / collectActiveIncidents все фильтруют
-- current_status='hard'. Без индекса каждый realert tick (раз в TickEverySec,
-- по умолчанию каждые 60s) и каждый клик в Fleet-Health делают full scan.
CREATE INDEX IF NOT EXISTS idx_incident_state_hard ON incident_state(user_id, check_name) WHERE current_status = 'hard';

CREATE TABLE IF NOT EXISTS daily_soft_flaps (
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    flap_count INTEGER NOT NULL DEFAULT 0,
    date TEXT NOT NULL,
    PRIMARY KEY (user_id, check_name, date),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS tg_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
