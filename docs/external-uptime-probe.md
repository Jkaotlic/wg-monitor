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
from the monitor's primary bot. This separates token revocation and bot/chat
configuration failures. It does **not** separate a Telegram-wide outage: for
provider-level independence use Option A with email or push outside Telegram.

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

## Wizard provisioning

The deploy wizard now models the probe as a separate third host. Open
`Service -> External readiness probe`, configure and preview the target, then
choose `Install / repair`. The same flows are available non-interactively:

```sh
wg-monitor-deploy external-probe preview
wg-monitor-deploy external-probe status
wg-monitor-deploy external-probe install
wg-monitor-deploy external-probe remove
```

Non-secret host metadata and the HTTPS `/readyz` URL are stored under
`[external_probe]` in `wizard.toml`. SSH and notification credentials remain in
the wizard secret store (`EXTERNAL_PROBE_*`). The remote notification env file
is mode `0600`; uploads first land in a private `mktemp` directory; and the
systemd service uses `DynamicUser`, a private temp directory, read-only system
paths, and `NoNewPrivileges`. The persisted health state contains only
`ok|down` and the failure count. SSH uses the dedicated `external-probe`
known-hosts alias, so a changed host key fails closed.

The installed systemd timer runs every minute. Each probe has a five-second
timeout, requires the exact body `ready`, pages after two consecutive failures,
deduplicates an ongoing outage, and sends a single recovery message. Use a bot
and chat independent from wg-monitor's primary alert path. If Telegram itself
is part of the outage you will not receive either Telegram notification, so a
hosted monitor with an email/push route remains the stronger design.

The wizard never chooses a host or installs this implicitly. Provisioning is an
explicit operator action because the host and notification channel must be
outside the backend failure domain.

## Future work

- Have agents observe a "backend silent for >N minutes" condition and send
  an out-of-band alert via their own direct TG client (currently every
  agent only reports through the backend).
