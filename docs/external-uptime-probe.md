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
| URL            | `https://<your-domain>/healthz`             |
| Interval       | 60s                                         |
| Expected body  | `ok`                                        |
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
URL="https://wgmon.example.com/healthz"
TG_TOKEN="<separate-bot-token-not-the-monitor's>"
TG_CHAT="<your-chat-id>"
STATE=/var/lib/wg-monitor-probe.last

body=$(curl -sS -m 5 "$URL" 2>/dev/null || echo FAIL)
if [ "$body" = "ok" ]; then
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
- Don't omit body matching (`ok`). A reverse proxy returning 200 with an
  empty body or a Caddy default page would pass a status-only check.

## Future work (not yet implemented)

- Have agents observe a "backend silent for >N minutes" condition and send
  an out-of-band alert via their own direct TG client (currently every
  agent only reports through the backend).
- Wizard menu item "Step 7: register external uptime probe" that walks
  the operator through Option A/B and saves the chosen channel into
  `state.db` for documentation.
