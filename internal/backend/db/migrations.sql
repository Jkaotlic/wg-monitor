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

CREATE TABLE IF NOT EXISTS daily_soft_flaps (
    user_id INTEGER NOT NULL,
    check_name TEXT NOT NULL,
    flap_count INTEGER NOT NULL DEFAULT 0,
    date TEXT NOT NULL,
    PRIMARY KEY (user_id, check_name, date),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
