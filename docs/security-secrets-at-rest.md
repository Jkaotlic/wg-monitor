# Secrets at rest — backend VPS (SEC-03)

This document records how the backend stores sensitive material on the VPS and
why **full-disk encryption (LUKS) is a deployment requirement**, not optional.

## What is stored, and where

| Data | File | Mode |
|------|------|------|
| Telegram bot token | `/etc/wg-monitor/bot-token.txt` | `0640 root:wgmonitor` |
| Wizard API token | `/etc/wg-monitor/wizard-token.txt` | `0640 root:wgmonitor` |
| Agent tokens (hashed), per-router AWGM auth | `state.db` (SQLite) | `0600 wgmonitor` |
| Amnezia Premium VPN keys / `vpn://` URIs | `/var/lib/wg-monitor/amnezia-premium.json` | `0600`, dir `0700` |
| HideMyName access codes | `/var/lib/wg-monitor/hidemyname.json` | `0600`, dir `0700` |
| Self-hosted VPN servers: SSH passwords | `/var/lib/wg-monitor/amnezia-selfhosted.json` | `0600`, dir `0700` |

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

Backlog (not in v0.38): encrypt these JSON files with the AES-GCM key already
used by agent revive; pin the self-hosted SSH host key (TOFU) instead of
`InsecureIgnoreHostKey`; revoke a peer on a self-hosted server.

## Why no application-level encryption

The relevant threat is read access to the VPS filesystem (LFI, a backup leak, a
stolen disk image, a compromised co-tenant). Encrypting these files *with a key
that also lives on the same VPS* does not defend against that threat — the
attacker who can read the ciphertext can read the key. It would add complexity
and a migration risk while providing only the appearance of protection.

The correct mitigation is **encryption of the volume itself**, where the key is
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

> Audit reference: SEC-03 (`docs/audit-2026-06-18.md`). App-level perms are in
> place and verified; this requirement closes the residual at-rest exposure.
