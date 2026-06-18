# External uptime probe (DEPLOY-30)

`wg-monitor-backend` already exposes `/healthz` (HTTP 200 + `ok`) and is
fronted by Caddy on the VPS. **Nothing inside the deployment monitors the
backend itself.** If the backend process dies, the systemd unit, Caddy, and
the monitor's own alerting are all in the same blast radius — so the only
signal users get is "alerts mysteriously stopped". That's a soft failure
mode and easy to miss for hours.

## Why the backend can't self-monitor

- A crashed process can't send a Telegram alert about its own crash.
- A network partition between VPS and Telegram makes the backend's "I am
  alive" message indistinguishable from "Telegram is unreachable".
- A SQLite corruption (DEPLOY-15-class) may keep the HTTP listener up while
  silently dropping every alert path — `/healthz` would still return 200.
- systemd `Restart=on-failure` doesn't fire for clean exits, hangs, or
  goroutine deadlocks where the listener accepts but never responds (see
  DEPLOY-12, DEPLOY-13).

You need a probe outside the failure domain.

## Recommended setup

## Which endpoint to probe: `/readyz`, not `/healthz`

The backend exposes two:

- **`/healthz`** — *liveness only*: returns `200` + JSON `{"status":"ok",...}` as
  long as the process accepts HTTP. It does **not** touch the database, so it
  stays `200` even when SQLite is corrupted and every alert is silently dropped.
  Use it only for Caddy upstream health.
- **`/readyz`** — *deep health*: runs an actual table read (`HealthCheck`), not a
  bare connection ping. Returns `200` + body `ready` when the DB is genuinely
  readable, and `503` when it isn't. This is the one that catches the
  "listener up, alerts dropped" silent-failure mode — **probe this one.**

Pick **one** of:

### Option A — uptime-kuma / healthchecks.io / dead-man-snitch (preferred)

Hosted/self-hosted dashboards already handle:
- alert routing (email + push + their own TG bot)
- alert deduplication
- maintenance windows
- public status page

Configure a check:

| Field          | Value                                       |
| -------------- | ------------------------------------------- |
| Type           | HTTP(S) keyword                             |
| URL            | `https://<your-domain>/readyz`              |
| Interval       | 60s                                         |
| Expected body  | `ready`                                     |
| Expected code  | 200                                         |
| Timeout        | 5s                                          |
| Retries        | 2 before paging                             |

### Option B — cron on a second host

If you already run a second VPS or a always-on home box (Raspberry Pi, NAS),
drop this in `/etc/cron.d/wg-monitor-probe`:

```cron
* * * * * root /usr/local/bin/wg-monitor-probe >/dev/null 2>&1
```

with `/usr/local/bin/wg-monitor-probe`:

```sh
#!/bin/sh
set -eu
URL="https://wgmon.example.com/readyz"   # deep check: 200 + "ready", 503 on DB failure
TG_TOKEN="<separate-bot-token-not-the-monitor's>"
TG_CHAT="<your-chat-id>"
STATE=/var/lib/wg-monitor-probe.last

# /readyz returns the literal body "ready" with HTTP 200 only when the DB is
# actually readable; any non-200 (incl. 503 on SQLite corruption) or transport
# error trips the alert.
body=$(curl -sS -m 5 -f "$URL" 2>/dev/null | tr -d '[:space:]' || echo FAIL)
if [ "$body" = "ready" ]; then
    [ -f "$STATE" ] && {
        # was failing, now recovered
        curl -sS -m 5 "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" \
            -d chat_id="$TG_CHAT" -d text="wg-monitor probe: RECOVERED ($URL)"
        rm -f "$STATE"
    }
    exit 0
fi

# failing
[ -f "$STATE" ] && exit 0   # already alerted, don't spam
echo "$(date -Iseconds) $body" > "$STATE"
curl -sS -m 5 "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" \
    -d chat_id="$TG_CHAT" -d text="wg-monitor probe: DOWN ($URL) — body=$body"
```

**Critical:** the bot token / chat used by the probe **must be different**
from the monitor's primary bot. Otherwise you've reintroduced the same
shared-failure-domain problem (token revocation, Telegram outage).

## What NOT to do

- Don't probe from the same VPS (`localhost:8080`). systemd will already
  catch a crashed process; you want failure-modes systemd misses.
- Don't probe from a Keenetic agent. Agents run on consumer flash and
  reboot when the user power-cycles the router; a missed probe ≠ dead
  backend.
- Don't omit body matching (`ready`). A reverse proxy returning 200 with an
  empty body or a Caddy default page would pass a status-only check.

## Quick install (Option B, copy-paste)

On a second always-on box (home Pi/NAS — NOT the backend VPS, NOT a Keenetic
agent), filling in the three values:

```sh
sudo tee /usr/local/bin/wg-monitor-probe >/dev/null <<'EOF'
# (paste the Option B script above)
EOF
sudo chmod +x /usr/local/bin/wg-monitor-probe
echo '* * * * * root /usr/local/bin/wg-monitor-probe >/dev/null 2>&1' | sudo tee /etc/cron.d/wg-monitor-probe
```

## In-process complement: the daily digest

The backend can also send a once-a-day "🟢 Монитор жив — N/M роутеров онлайн"
message to the primary chat (`digest.enabled: true`, `digest.hour_msk: 9` in
`backend.yaml`). Its **absence** at the expected time is itself a signal that
the backend is down. This is a soft, zero-extra-infra complement — it shares the
backend's failure domain, so it does **not** replace an external probe; it just
makes silence noticeable for operators who haven't set one up yet.

## Future work (not yet implemented)

- **Wizard auto-provisioning of the external probe** (deferred): a "register
  external uptime probe" step that SSHes to a second host, uploads
  `wg-monitor-probe`, and writes the cron. Deferred because the wizard currently
  models only agent (router) and backend (VPS) SSH targets — the probe host is a
  third target type with its own credentials/known-hosts, which is a meaningful
  addition. Until then, use the copy-paste above.
- Have agents observe a "backend silent for >N minutes" condition and send
  an out-of-band alert via their own direct TG client (currently every
  agent only reports through the backend).
