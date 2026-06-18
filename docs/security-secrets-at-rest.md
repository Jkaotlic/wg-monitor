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

File permissions are enforced in code (`amnezia_secrets.go`, `hidemy_secrets.go`
create the dir `0700` and write the file `0600` atomically; the SQLite DB is
installed `0600`). These perms protect against *other local users*, which on a
single-purpose VPS is the common case.

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
