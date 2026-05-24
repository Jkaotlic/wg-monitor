package db

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrUserNotFound = errors.New("user not found")

const (
	KindStatic = "static"
	KindMobile = "mobile"
)

func IsValidKind(k string) bool { return k == KindStatic || k == KindMobile }

type User struct {
	ID               int64
	Nickname         string
	TokenHash        string
	ExpectedExitIP   string
	AWGIface         string
	Kind             string
	TelegramThreadID *int64
	// TelegramUserID is the numeric Telegram user id of the router's owner,
	// captured either via CLI bind-tg-user or via TOFU on the first action
	// in the owner's per-router topic. NULL until bound; callbacks router
	// uses it to gate non-admin button taps to the rightful owner.
	TelegramUserID *int64
	CreatedAt      time.Time
	LastSeenAt     *time.Time
	// Wizard-sync deploy metadata (v0.12.0). All NULL for routers added
	// before sync existed. Filled by PUT /v1/wizard/agents/{nickname} after
	// a successful wizard deploy. NEVER read by the agent — wizard-only.
	SSHHost             *string
	SSHPort             *int64
	SSHUser             *string
	Arch                *string
	LastDeployedVersion *string
	Ring                *string
	PendingVersion      *string
	PendingSince        *string
	LastDeploy          *string
	DeployMode          *string
	AWGMURL             *string
	AWGMAuth            *string
	ExpectedMAC         *string
}

// IsMobile reports whether this user is a mobile (4G in-vehicle) router.
// Used by the heartbeat watcher to apply a longer grace window.
func (u User) IsMobile() bool { return u.Kind == KindMobile }

type UsersRepo struct{ d *DB }

func (d *DB) Users() *UsersRepo { return &UsersRepo{d: d} }

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Insert creates a static-kind user (default for fixed home/office routers).
// Use InsertWithKind to create a mobile-kind user.
func (u *UsersRepo) Insert(nickname, rawToken, expectedExitIP, awgIface string) (int64, error) {
	return u.InsertWithKind(nickname, rawToken, expectedExitIP, awgIface, KindStatic)
}

func (u *UsersRepo) InsertWithKind(nickname, rawToken, expectedExitIP, awgIface, kind string) (int64, error) {
	if !IsValidKind(kind) {
		return 0, fmt.Errorf("users.Insert: invalid kind %q (want static|mobile)", kind)
	}
	res, err := u.d.db.Exec(
		`INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface, kind) VALUES (?, ?, ?, ?, ?)`,
		nickname, hashToken(rawToken), expectedExitIP, awgIface, kind,
	)
	if err != nil {
		return 0, fmt.Errorf("users.Insert: %w", err)
	}
	return res.LastInsertId()
}

// UpsertEnrollment creates or rotates a router's raw-token enrollment from the
// trusted deploy wizard. It preserves an existing Telegram topic when threadID
// is omitted so VPS migrations do not detach already-linked router panels.
func (u *UsersRepo) UpsertEnrollment(nickname, rawToken, kind string, threadID int64) (int64, error) {
	if !IsValidKind(kind) {
		return 0, fmt.Errorf("users.UpsertEnrollment: invalid kind %q (want static|mobile)", kind)
	}
	_, err := u.d.db.Exec(`
INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id)
VALUES (?, ?, '0.0.0.0', 'awg0', ?, NULLIF(?, 0))
ON CONFLICT(nickname) DO UPDATE SET
  token_hash=excluded.token_hash,
  kind=excluded.kind,
  telegram_thread_id=COALESCE(excluded.telegram_thread_id, users.telegram_thread_id)
`, nickname, hashToken(rawToken), kind, threadID)
	if err != nil {
		return 0, fmt.Errorf("users.UpsertEnrollment: %w", err)
	}
	got, err := u.GetByNickname(nickname)
	if err != nil {
		return 0, err
	}
	return got.ID, nil
}

// userColsFull lists every column read by single-row Get*. GetAll uses a
// shorter projection (no token_hash, no created_at) and has its own scanner.
const userColsFull = `id, nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, created_at, last_seen_at, ssh_host, ssh_port, ssh_user, arch, last_deployed_version, deploy_ring, pending_version, pending_since, last_deploy, deploy_mode, awgm_url, awgm_auth, expected_mac`

type userScanner interface {
	Scan(dest ...any) error
}

func scanUserFull(s userScanner) (*User, error) {
	var got User
	var threadID sql.NullInt64
	var tgUserID sql.NullInt64
	var lastSeen sql.NullTime
	var sshHost sql.NullString
	var sshPort sql.NullInt64
	var sshUser sql.NullString
	var arch sql.NullString
	var lastDepVer sql.NullString
	var ring sql.NullString
	var pendingVersion sql.NullString
	var pendingSince sql.NullString
	var lastDeploy sql.NullString
	var deployMode sql.NullString
	var awgmURL sql.NullString
	var awgmAuth sql.NullString
	var expectedMAC sql.NullString
	if err := s.Scan(
		&got.ID, &got.Nickname, &got.TokenHash, &got.ExpectedExitIP, &got.AWGIface, &got.Kind,
		&threadID, &tgUserID, &got.CreatedAt, &lastSeen,
		&sshHost, &sshPort, &sshUser, &arch, &lastDepVer, &ring, &pendingVersion, &pendingSince,
		&lastDeploy, &deployMode, &awgmURL, &awgmAuth, &expectedMAC,
	); err != nil {
		return nil, err
	}
	if threadID.Valid {
		v := threadID.Int64
		got.TelegramThreadID = &v
	}
	if tgUserID.Valid {
		v := tgUserID.Int64
		got.TelegramUserID = &v
	}
	if lastSeen.Valid {
		v := lastSeen.Time
		got.LastSeenAt = &v
	}
	if sshHost.Valid {
		v := sshHost.String
		got.SSHHost = &v
	}
	if sshPort.Valid {
		v := sshPort.Int64
		got.SSHPort = &v
	}
	if sshUser.Valid {
		v := sshUser.String
		got.SSHUser = &v
	}
	if arch.Valid {
		v := arch.String
		got.Arch = &v
	}
	if lastDepVer.Valid {
		v := lastDepVer.String
		got.LastDeployedVersion = &v
	}
	if ring.Valid {
		v := ring.String
		got.Ring = &v
	}
	if pendingVersion.Valid {
		v := pendingVersion.String
		got.PendingVersion = &v
	}
	if pendingSince.Valid {
		v := pendingSince.String
		got.PendingSince = &v
	}
	if lastDeploy.Valid {
		v := lastDeploy.String
		got.LastDeploy = &v
	}
	if deployMode.Valid {
		v := deployMode.String
		got.DeployMode = &v
	}
	if awgmURL.Valid {
		v := awgmURL.String
		got.AWGMURL = &v
	}
	if awgmAuth.Valid {
		v := awgmAuth.String
		got.AWGMAuth = &v
	}
	if expectedMAC.Valid {
		v := expectedMAC.String
		got.ExpectedMAC = &v
	}
	return &got, nil
}

func (u *UsersRepo) GetByToken(rawToken string) (*User, error) {
	target := hashToken(rawToken)
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE token_hash = ?`, target)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(got.TokenHash), []byte(target)) != 1 {
		// SHA-256 collision is astronomically unlikely; this branch is paranoia.
		return nil, ErrUserNotFound
	}
	return got, nil
}

func (u *UsersRepo) GetByID(id int64) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE id = ?`, id)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}

func (u *UsersRepo) GetByNickname(nickname string) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE nickname = ?`, nickname)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}

func (u *UsersRepo) GetAll() ([]User, error) {
	rows, err := u.d.db.Query(
		`SELECT id, nickname, expected_exit_ip, awg_iface, kind, telegram_thread_id, telegram_user_id, created_at, last_seen_at, ssh_host, ssh_port, ssh_user, arch, last_deployed_version, deploy_ring, pending_version, pending_since, last_deploy, deploy_mode, awgm_url, awgm_auth, expected_mac FROM users ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var got User
		var threadID sql.NullInt64
		var tgUserID sql.NullInt64
		var lastSeen sql.NullTime
		var sshHost sql.NullString
		var sshPort sql.NullInt64
		var sshUser sql.NullString
		var arch sql.NullString
		var lastDepVer sql.NullString
		var ring sql.NullString
		var pendingVersion sql.NullString
		var pendingSince sql.NullString
		var lastDeploy sql.NullString
		var deployMode sql.NullString
		var awgmURL sql.NullString
		var awgmAuth sql.NullString
		var expectedMAC sql.NullString
		if err := rows.Scan(
			&got.ID, &got.Nickname, &got.ExpectedExitIP, &got.AWGIface, &got.Kind,
			&threadID, &tgUserID, &got.CreatedAt, &lastSeen,
			&sshHost, &sshPort, &sshUser, &arch, &lastDepVer, &ring, &pendingVersion, &pendingSince,
			&lastDeploy, &deployMode, &awgmURL, &awgmAuth, &expectedMAC,
		); err != nil {
			return nil, err
		}
		if threadID.Valid {
			v := threadID.Int64
			got.TelegramThreadID = &v
		}
		if tgUserID.Valid {
			v := tgUserID.Int64
			got.TelegramUserID = &v
		}
		if lastSeen.Valid {
			v := lastSeen.Time
			got.LastSeenAt = &v
		}
		if sshHost.Valid {
			v := sshHost.String
			got.SSHHost = &v
		}
		if sshPort.Valid {
			v := sshPort.Int64
			got.SSHPort = &v
		}
		if sshUser.Valid {
			v := sshUser.String
			got.SSHUser = &v
		}
		if arch.Valid {
			v := arch.String
			got.Arch = &v
		}
		if lastDepVer.Valid {
			v := lastDepVer.String
			got.LastDeployedVersion = &v
		}
		if ring.Valid {
			v := ring.String
			got.Ring = &v
		}
		if pendingVersion.Valid {
			v := pendingVersion.String
			got.PendingVersion = &v
		}
		if pendingSince.Valid {
			v := pendingSince.String
			got.PendingSince = &v
		}
		if lastDeploy.Valid {
			v := lastDeploy.String
			got.LastDeploy = &v
		}
		if deployMode.Valid {
			v := deployMode.String
			got.DeployMode = &v
		}
		if awgmURL.Valid {
			v := awgmURL.String
			got.AWGMURL = &v
		}
		if awgmAuth.Valid {
			v := awgmAuth.String
			got.AWGMAuth = &v
		}
		if expectedMAC.Valid {
			v := expectedMAC.String
			got.ExpectedMAC = &v
		}
		out = append(out, got)
	}
	return out, rows.Err()
}

func (u *UsersRepo) UpdateLastSeen(id int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (u *UsersRepo) UpdateThreadID(id, threadID int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET telegram_thread_id = ? WHERE id = ?`, threadID, id)
	return err
}

// SetTelegramUserID binds the router to a specific Telegram user id.
// Used by the callbacks router (TOFU on first owner action) and the
// `bind-tg-user` CLI. Pass 0 to clear the binding.
func (u *UsersRepo) SetTelegramUserID(id, tgUserID int64) error {
	if tgUserID == 0 {
		_, err := u.d.db.Exec(`UPDATE users SET telegram_user_id = NULL WHERE id = ?`, id)
		return err
	}
	_, err := u.d.db.Exec(`UPDATE users SET telegram_user_id = ? WHERE id = ?`, tgUserID, id)
	return err
}

// ClearThreadID nulls out telegram_thread_id for a user. Used by the
// dispatcher's self-heal path: when sendMessage reports the cached topic
// no longer exists in TG, we clear the id so the next ensureTopic call
// invokes createForumTopic and persists a fresh id.
func (u *UsersRepo) ClearThreadID(id int64) error {
	_, err := u.d.db.Exec(`UPDATE users SET telegram_thread_id = NULL WHERE id = ?`, id)
	return err
}

// GetByThreadID looks up a user by their assigned Telegram forum-topic id.
// Used by the callbacks router to map an incoming Message's
// message_thread_id to the owning user (per-router topic). Returns
// ErrUserNotFound when no user owns this topic.
func (u *UsersRepo) GetByThreadID(threadID int64) (*User, error) {
	row := u.d.db.QueryRow(`SELECT `+userColsFull+` FROM users WHERE telegram_thread_id = ?`, threadID)
	got, err := scanUserFull(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return got, nil
}

// DeployInfo carries the wizard-side metadata pushed via
// PUT /v1/wizard/agents/{nickname}. All fields required; empty strings or
// zero port are rejected by the handler before reaching here.
type DeployInfo struct {
	SSHHost             string
	SSHPort             int64
	SSHUser             string
	Arch                string
	LastDeployedVersion string
	Ring                string
	PendingVersion      string
	PendingSince        string
	LastDeploy          string
	DeployMode          string
	AWGMURL             string
	AWGMAuth            string
	ExpectedMAC         string
}

// UpdateDeployInfo upserts the wizard-side deploy fields by nickname. Returns
// ErrUserNotFound when no row matches (we do NOT auto-create — agent
// enrollment goes through the existing wg-monitor-cli add-user path).
func (u *UsersRepo) UpdateDeployInfo(nickname string, info DeployInfo) error {
	res, err := u.d.db.Exec(
		`UPDATE users SET ssh_host=?, ssh_port=?, ssh_user=?, arch=?, last_deployed_version=?, deploy_ring=?, pending_version=?, pending_since=?, last_deploy=?, deploy_mode=?, awgm_url=?, awgm_auth=?, expected_mac=? WHERE nickname=?`,
		info.SSHHost, info.SSHPort, info.SSHUser, info.Arch, info.LastDeployedVersion,
		nullEmpty(info.Ring), nullEmpty(info.PendingVersion), nullEmpty(info.PendingSince),
		nullEmpty(info.LastDeploy), nullEmpty(info.DeployMode), nullEmpty(info.AWGMURL),
		nullEmpty(info.AWGMAuth), nullEmpty(info.ExpectedMAC), nickname,
	)
	if err != nil {
		return fmt.Errorf("users.UpdateDeployInfo: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateLastSeenAgentVersion advances users.last_deployed_version to the
// version reported by the running agent in its latest heartbeat. If the
// heartbeat matches a pending wizard deploy, it also clears the pending marker
// so a delayed post-update heartbeat does not leave the wizard stuck forever.
// The WHERE clause skips the write when no visible deploy state changes,
// important because /v1/report fires every 60s per agent and SQLite is
// single-writer.
func (u *UsersRepo) UpdateLastSeenAgentVersion(id int64, version string) error {
	if version == "" {
		return nil
	}
	_, err := u.d.db.Exec(
		`UPDATE users
		    SET last_deployed_version = ?,
		        pending_since = CASE WHEN pending_version = ? THEN NULL ELSE pending_since END,
		        pending_version = CASE WHEN pending_version = ? THEN NULL ELSE pending_version END
		  WHERE id = ?
		    AND (COALESCE(last_deployed_version, '') != ? OR pending_version = ?)`,
		version, version, version, id, version, version,
	)
	if err != nil {
		return fmt.Errorf("users.UpdateLastSeenAgentVersion: %w", err)
	}
	return nil
}

// HasAnyOperatorOrOwnerBinding reports whether the given Telegram
// user id is bound to any router as owner (users.telegram_user_id)
// or listed in any router_operators row. Used by /help to pick
// admin vs operator vs none content.
func (u *UsersRepo) HasAnyOperatorOrOwnerBinding(tgUserID int64) (bool, error) {
	var one int
	err := u.d.db.QueryRow(
		`SELECT 1 WHERE EXISTS (SELECT 1 FROM users WHERE telegram_user_id = ?)
		            OR EXISTS (SELECT 1 FROM router_operators WHERE telegram_user_id = ?)`,
		tgUserID, tgUserID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("HasAnyOperatorOrOwnerBinding: %w", err)
	}
	return true, nil
}
