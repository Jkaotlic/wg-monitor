# Secrets at rest — backend VPS (SEC-03)

This document records how the backend stores sensitive material on the VPS and
why **full-disk encryption (LUKS) is a deployment requirement**, not optional.

## What is stored, and where

| Data | File | Mode |
|------|------|------|
| Telegram bot token | `/etc/wg-monitor/bot-token.txt` | `0640 root:wgmonitor` |
| Wizard API token | `/etc/wg-monitor/wizard-token.txt` | `0640 root:wgmonitor` |
| Agent tokens (hashed), per-router AWGM auth | `state.db` (SQLite) | `0600 wgmonitor` |
| Amnezia Premium VPN keys / `vpn://` URIs | `amnezia-premium.json` next to `db_path` | `0600`, dir `0700`, encrypted with `revive.key` (v0.55) |
| HideMyName access codes | `hidemyname.json` next to `db_path` | `0600`, dir `0700`, encrypted with `revive.key` (v0.55) |
| Self-hosted VPN servers: SSH passwords | `amnezia-selfhosted.json` next to `db_path` | `0600`, dir `0700`, encrypted with `revive.key` (v0.55) |
| awg3 panels: panel password, client certificate and private key from the `.p12` | `awg3-panels.json` next to `amnezia-selfhosted.json` | `0600`, dir `0700`, encrypted with `revive.key` (v0.55) |
| Root passwords / awg-manager logins for agent revive | `state.db`, table `revive_secrets` / `router_credentials` | AES-256-GCM with `revive.key`, AAD = router id |

Until v0.52.2 the four JSON stores defaulted to `/var/lib/wg-monitor/`; since
then they default to the directory of `db_path` (the Docker volume) and are
moved there once on startup.

File permissions are enforced in code (`amnezia_secrets.go`, `hidemy_secrets.go`, `selfhostedamnezia/provider.go`
create the dir `0700` and write the file `0600` atomically; the SQLite DB is
installed `0600`). These perms protect against *other local users*, which on a
single-purpose VPS is the common case.

## How cabinet secrets get in (v0.38)

- Amnezia Premium `vpn://` keys, HideMy.name access codes and self-hosted SSH
  passwords are entered **only** in the mini app / web control, over HTTPS.
  The bot no longer accepts them as chat text; a `vpn://` key or
  `ssh_password=` pasted into a chat the bot sees (private chat or an allowed
  group) is deleted by the bot, which replies with a pointer to the app. A
  10-20 digit code is deleted only in a private chat with the bot: in groups
  such numbers are phone numbers and Telegram IDs and are left alone.
- API responses never return a secret: keys and codes are shown as a
  4-character mask, the SSH password only as `password_set`. Request bodies
  that carry secrets print as `[скрыто]` and are never logged; cabinet errors
  are redacted before logging.
- A key or code is checked against the provider before it is stored (Amnezia
  login + account, HideMy server list). The SSH password is **not** checked on
  save; "Check connection" makes one SSH attempt on demand.
- A legacy `amnezia_selfhosted.ssh_password` in `backend.yaml` is copied into
  the self-hosted file once at startup; the backend logs a warning on every
  start until it is removed from the YAML.
- All three JSON stores are written under a per-file lock (atomic
  temp + rename, `0600`); issuing a config on a self-hosted server holds a
  per-server lock so two clients never get the same address.

Known limits of the chat guard (not fixed in v0.38):

- Edited messages are not checked: the bot does not receive `edited_message`
  updates, so a secret added by editing an old message stays in the chat.
- A code is recognised only as a message (or file caption) that is nothing but
  10-20 digits. A code with spaces or other text around it is not deleted.
- The guard sees only chats the bot is in; a secret sent anywhere else is
  outside its reach.

## Cabinet stores encrypted with `revive.key` (v0.55)

Operator decision 05.10.2026: the four cabinet JSON stores are encrypted with
**the same key that protects saved root passwords** (`revive.key_file`,
`revive.Box`). No separate key, no new cryptography:

- **Cipher:** AES-256-GCM from `revive.Box` (`SealBlob`/`OpenBlob`). The AAD
  is `wg-monitor-file:` + the store's canonical name (`amnezia-premium.json`,
  `hidemyname.json`, `amnezia-selfhosted.json`, `awg3-panels.json`), so the
  ciphertext of one store cannot be swapped in for another, and can never be
  confused with a root-password record (whose AAD is the bare router id).
  The root-password format is unchanged.
- **File format:** first line `wg-monitor-sealed v1`, then one line of
  base64(nonce ‖ ciphertext). Plain JSON starts with `{` or whitespace, so the
  two are told apart unambiguously; a future `wg-monitor-sealed v2` is still
  recognised as "encrypted, unknown version" rather than as broken JSON.
  Code: `internal/backend/sealedfile`.
- **Writes:** temp file `0600` in the same directory → `fsync` → `rename` →
  `chmod 0600` → `fsync` of the directory. With the key every write is
  encrypted.
- **First start with the key** re-encrypts plain stores in place, before
  anything opens them (`backend.SealCabinetStores` in `cmd/backend/main.go`).
  A second start finds them encrypted and leaves them byte-for-byte as they
  are. Plain files are still readable with the key present (before migration,
  or if a re-encryption failed — the log says which store).
- **No key** (`revive.key_file` unset or unreadable): the backend starts as
  before, stores stay plain JSON, the log gets a warning and the Park screen
  shows it in the "Бэкенд" card (`/v1/miniapp/fleet` → `backend.secrets_warning`).
  If there are no store files at all, there is nothing to warn about.
- **Encrypted store, key missing or wrong:** the cabinet screens say so in
  words ("ключ шифрования не найден" / "ключ шифрования не тот"; codes
  `cabinet_key_missing` / `cabinet_key_wrong`, HTTP 503) instead of "key not
  saved", and **no write goes over an encrypted file without the key** — the
  file is left untouched. Bring the old `revive.key` back and restart.
- **Backup:** encrypted stores go into the archive as they are. The key stays
  **out** of the archive (variant A, `includeReviveKey = false`): a leaked
  archive decrypts neither root passwords nor cabinet keys. `backup verify`
  decrypts the archived stores with this machine's key and checks the JSON
  inside; without a key it passes but prints that after a restore the cabinet
  keys cannot be read until the old `revive.key` is put back; with a different
  key it fails. **Keep a copy of `revive.key` outside the server and outside
  the backups** — losing it means re-entering every cabinet key, code and
  password.

Remaining backlog: pin the self-hosted SSH host key (TOFU) instead of
`InsecureIgnoreHostKey`; revoke a peer on a self-hosted server.

## What application-level encryption does and does not buy

The relevant threat is read access to the VPS filesystem (LFI, a backup leak, a
stolen disk image, a compromised co-tenant). Encrypting these files *with a key
that also lives on the same VPS* does not defend against an attacker who can
read the whole disk — the key is readable too. What it does buy: a backup
archive, a copied data volume or a stray copy of a store file no longer
carries the cabinet secrets in clear — the key is never in the archives, and
as long as `revive.key_file` points outside the data volume, not on it either. The full-disk threat still needs:

The correct mitigation for that is **encryption of the volume itself**, where the key is
supplied at boot and never written to the protected disk:

- **Full-disk / volume encryption (LUKS)** on the partition holding
  `/var/lib/wg-monitor` and `/etc/wg-monitor`. This is the recommended baseline.
- Keep the VPS single-purpose; restrict shell access; ship `state.db` backups
  only through the wizard's existing encrypted-backup path
  (argon2id + XChaCha20-Poly1305), never as plaintext copies.

## Operator checklist

- [ ] Backend VPS root/data volume is LUKS-encrypted (or provider volume encryption with an externally-held key).
- [ ] No unencrypted off-host copies of `state.db` / the JSON secret files exist (use the wizard's encrypted backup).
- [ ] Only `root` and the `wgmonitor` service user can read `/etc/wg-monitor` and `/var/lib/wg-monitor`.
- [ ] `revive.key_file` is set, points outside the data volume, and a copy of the key is kept off the server (not in the backups). The Park "Бэкенд" card shows no cabinet-keys warning.

> Audit reference: SEC-03 (`docs/audit-2026-06-18.md`). App-level perms are in
> place and verified; this requirement closes the residual at-rest exposure.
