# Tunnel Import via Telegram — Design

**Date:** 2026-05-06  
**Status:** Approved  
**Scope:** Agent does full import; backend handles TG file download + stateful naming dialog.

---

## 1. Use Case

Admin replaces a dead AWG tunnel by sending a new `.conf` file to the per-router TG topic. Bot auto-configures the new tunnel via awg-manager, deletes the old one, and restarts HydraRoute if installed. No SSH required.

---

## 2. Data Flow

```
Admin → .conf Document in per_router TG topic
  backend: HandleMessage detects Message.Document
  backend: GetFile(file_id) → DownloadFile(file_path) [limit 50 KB]
  backend: extract tunnel name from filename (strip .conf, sanitize)
    ├─ valid (^[a-z][a-z0-9_-]{0,31}$)  → store pending, send confirmation buttons
    └─ invalid                           → "Как назвать?" → user types name → then buttons

  Confirmation inline keyboard (callback_data stored with short token in pendingUploads):
    [🔄 Заменить сущ. <name>]  → tunnel_import_replace:<uid>:<token8>
    [➕ Добавить новый]         → tunnel_import_add:<uid>:<token8>

  backend: EnqueueWithRef(tunnel_import, {conf:base64, name:"awg11", replace:true}, ref)

  agent (via long-poll):
    1. decode base64 conf
    2. parse WireGuard+AmneziaWG ini format
    3. GET /api/tunnels/all
    4. if replace=true: find tunnel by Name field → save old_id + old.DefaultRoute
    5. POST /api/tunnels/create (defaultRoute=old.DefaultRoute or true if not found, enabled=true)
    6. if create OK and old found: DELETE /api/tunnels/{old_id}
    7. GET /api/system/hydraroute-status → if Installed: exec /opt/etc/init.d/S99hrneo restart
    8. return CommandResult with summary

  backend: Notifier relays CommandResult to TG (already wired via EnqueueWithRef)
```

---

## 3. Components Changed

| File | Change |
|------|--------|
| `internal/backend/tg/updates.go` | Add `Document *Document` to `Message`; new `Document` struct |
| `internal/backend/tg/client.go` | Add `GetFile`, `DownloadFile` methods; expose `Token` for file URL |
| `internal/backend/callbacks/router.go` | Document handler, pending-name state, `handleConfirmImport` |
| `internal/backend/callbacks/parse.go` | Add `tunnel_import_replace`, `tunnel_import_add` to validActions; parse token field |
| `internal/backend/callbacks/actions.go` | Add `ImportAction` (lookup pending, enqueue wire.Command) |
| `internal/backend/callbacks/router.go` (TGClient iface) | Add `GetFile`, `DownloadFile` to interface |
| `pkg/wire/types.go` | Add `"tunnel_import"` to validCommandActions |
| `internal/agent/awgmgr/types.go` | Add `CreateTunnelRequest`, `InterfaceConfig`, `PeerConfig` structs |
| `internal/agent/awgmgr/client.go` | Add `CreateTunnel`, `DeleteTunnel` methods |
| `internal/agent/actions/tunnel_import.go` | New: WG conf parser + full import action |
| `internal/agent/actions/runner.go` | Add `tunnel_import` case |

---

## 4. WG Conf Parser

Parses standard WireGuard + AmneziaWG ini format. Handles both `[Interface]` and `[Peer]` sections. Fields:

**Interface:** `PrivateKey`, `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1`, `H2`, `H3`, `H4`  
**Peer:** `PublicKey`, `PresharedKey`, `Endpoint`, `AllowedIPs` (comma-separated → []string)  
**Ignored:** `Address`, `DNS`, `PersistentKeepalive` (managed by Keenetic, not awg-manager)

Type = `"amnezia_wg"` always (all user tunnels are AWG).

---

## 5. awg-manager API

`POST /api/tunnels/create` — body:
```json
{
  "name": "awg11",
  "type": "amnezia_wg",
  "interface": {"privateKey":"...","jc":4,"jmin":40,"jmax":70,"s1":0,"s2":0,"h1":1,"h2":2,"h3":3,"h4":4},
  "peer": {"publicKey":"...","presharedKey":"...","endpoint":"host:port","allowedIPs":["0.0.0.0/0"]},
  "defaultRoute": true,
  "enabled": true
}
```

`DELETE /api/tunnels/{id}` — removes tunnel by awg-manager UUID.

Both require header `X-Requested-With: XMLHttpRequest`.

---

## 6. HydraRoute Rebinding

HydraRoute binds to tunnels via Keenetic NDMS routing tables (table 4096 → active VPN iface). After tunnel replacement, NDMS auto-updates. HydraRoute daemon (`hrneo`) needs restart to pick up the new interface:

```
exec /opt/etc/init.d/S99hrneo restart
```

Agent detects HydraRoute via existing `awgmgr.HydraRouteStatus().Installed`. If not installed → skip restart.

---

## 7. Pending State (in-memory)

```go
type pendingUpload struct {
    ConfB64    string
    Name       string    // empty = waiting for user to type name
    ThreadID   *int64
    Token      string    // 8-char hex, used in callback_data
    ExpiresAt  time.Time // time.Now() + 5min
}
// map key: userID (int64)
```

Cleaned up on each document/message handler call (check ExpiresAt).

---

## 8. Callback Format

`tunnel_import_replace:<uid>:<token8>` — replace existing tunnel  
`tunnel_import_add:<uid>:<token8>` — add as new (no deletion)

Token is 8 hex chars (first 4 bytes of `crypto/rand`). Total length ≤ 35 chars, well under TG 64-byte limit.

---

## 9. Operation Order (Safety)

1. POST create → on error: return error, old tunnel unchanged
2. DELETE old (only on create success)
3. Restart hrneo (if installed)

---

## 10. Security

- Document handling only in per_router topic (resolveTopicKind == "per_router")
- Admin-only gate already in HandleMessage (AdminUserID check)
- File size > 50 KB → reject immediately (WG conf is always < 2 KB)
- Pending state expires in 5 min
