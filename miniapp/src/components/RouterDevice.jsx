import { useId } from 'preact/hooks'
import { checkLabel, tunnelStateLabel, trafficLabel } from '../labels.js'

// The drawing IS the instrument: antennas are tunnels, lamps are checks, and the
// traffic path shows where packets actually leave. Nothing here is decorative --
// every element is bound to state the operator asked to see at a glance.
//
// The geometry is PORTED, not redrawn. Source: the operator-approved mockup at
// docs/superpowers/specs/assets/2026-07-17-router-device-mockup-a.html. That file
// hand-authored two antennas; the constants below were reverse-engineered from its
// own numbers and reproduce its path data digit-for-digit at two tunnels (verified:
// the r=9/14/19 field arcs come out to "M 49.14 23.44 A 9 9 0 0 1 61.08 16.54" and
// friends, exactly the mockup's strings). Everything the mockup hardcoded -- which
// antenna radiates, which lamp is red -- is a prop here; nothing about the object
// itself was re-invented.

// ---------------------------------------------------------------------------
// Geometry constants, all lifted from the mockup's own coordinates.
// ---------------------------------------------------------------------------
const SHELL = { x: 18, y: 108, w: 312, h: 126, rx: 14 }
const CENTER = 174 // shell centre line: 18 + 312/2
const TOP = 108 // shell top edge -- where the traffic path leaves the body

// Antennas. The mockup put its two bases at x=82 and x=266 (CENTER +/- 92) and
// leaned each one outboard by atan(24/86) -- the exact rise/run of its mast line
// (82,113)->(58,27). Field arcs are centred 30deg outboard of the shell's vertical
// (90 - 30t) and span 100deg. These four numbers regenerate the mockup exactly.
const ANT_SPAN = 92
const ANT_LEAN = Math.atan2(24, 86) // radians from vertical, per unit of t
const MAST_LEN = Math.hypot(24, 86)
const MAST_BASE_Y = 113
const COLLAR_Y = 111
const TIP_GAP = 2 // the tip cap sits 2px above the mast's end, as in the mockup
const ARC_HALF = 50
const ARC_OUTBOARD = 30
// A dead antenna droops. This is the one deliberate geometric ADDITION to the
// mockup's antenna: a non-colour cue for "not working", so the fault survives
// greyscale, colour-blindness, and a frozen (reduced-motion) drawing.
const DROOP = 13

// Indicator panel. The mockup's six lamps sat at cx 56..292 on an even pitch;
// keeping that span means a five-lamp row (the real check set) lands on the same
// rail the operator approved.
const PANEL = { x: 32, y: 148, w: 284, h: 48, rx: 7 }
const LAMP_Y = 166
const LAMP_FIRST = 56
const LAMP_LAST = 292

// The WAN port is an ADDITION -- the mockup had no port, because it had no traffic
// path to route through one. It is drawn as a stub on the shell's right edge (a
// connector protruding from the side is visible in a front view) and is ALWAYS
// present: a physical port does not appear and disappear with the routing mode.
// Only the path through it lights up.
const WAN = { x: 330, y: 208, w: 10, h: 14, cy: 215 }

const rad = (deg) => (deg * Math.PI) / 180
const r2 = (n) => Math.round(n * 100) / 100

function polar(cx, cy, r, deg) {
  return [cx + r * Math.cos(rad(deg)), cy - r * Math.sin(rad(deg))]
}

// One radiating field arc, centred on the antenna tip.
function fieldArc(tipX, tipY, r, centreDeg) {
  const [x0, y0] = polar(tipX, tipY, r, centreDeg + ARC_HALF)
  const [x1, y1] = polar(tipX, tipY, r, centreDeg - ARC_HALF)
  return `M ${r2(x0)} ${r2(y0)} A ${r} ${r} 0 0 1 ${r2(x1)} ${r2(y1)}`
}

// Antenna slots. t runs -1..1 across the shell; at two tunnels it yields exactly
// the mockup's -1 and +1, i.e. bases at 82 and 266. A lone tunnel stands upright
// dead centre.
function antennaSlots(count) {
  return Array.from({ length: count }, (_, i) => {
    const t = count === 1 ? 0 : (i / (count - 1)) * 2 - 1
    return { t, baseX: CENTER + t * ANT_SPAN }
  })
}

// Where an "up and out" path may rise without touching a mast.
//
// This is load-bearing, not cosmetic: `unknown` must never point at a tunnel, and
// a path drawn up the centre would lie the moment an odd tunnel count puts an
// antenna there. So the lane is the middle of the widest gap between masts. At two
// tunnels that is x=174 -- the mockup's centre line -- and at three it steps aside.
function freeLaneX(baseXs) {
  const walls = [SHELL.x + 22, ...baseXs, SHELL.x + SHELL.w - 22].sort((a, b) => a - b)
  let best = CENTER
  let bestGap = -1
  for (let i = 0; i < walls.length - 1; i++) {
    const gap = walls[i + 1] - walls[i]
    if (gap > bestGap) {
      bestGap = gap
      best = (walls[i] + walls[i + 1]) / 2
    }
  }
  return best
}

// Short silkscreen keys, the way a real panel is actually printed. Exported so the
// legend plate can bind its rows to these same lamps by key rather than inventing
// a second vocabulary -- the mockup's whole legend conceit is "same order, same
// colours, same keys", and two key maps would drift.
export const LAMP_KEYS = {
  dns: 'DNS',
  external_reach: 'EXT',
  hydraroute: 'HR',
  awg_manager: 'AWGM',
  tunnels: 'TUN',
}

export function lampKey(name) {
  if (LAMP_KEYS[name]) return LAMP_KEYS[name]
  // An unrecognised check still gets a lamp: silkscreen is short by nature, and the
  // legend plate carries the full name. Truncating beats dropping the lamp.
  return (name ?? '').split('_')[0].slice(0, 4).toUpperCase() || '?'
}

// Lamp order is fixed, not incoming-order: an instrument whose lamps reshuffle
// between polls is not an instrument. This order reproduces the approved panel
// (DNS, EXT, HR, AWGM, TUN); anything unrecognised sorts after, alphabetically.
const LAMP_ORDER = ['dns', 'external_reach', 'hydraroute', 'awg_manager', 'tunnels']

function orderChecks(checks) {
  // `tunnel_*` rows are excluded on purpose: those are the antennas. The events
  // endpoint carries them in `checks` too (miniapp_handler.go:238-245 appends every
  // row, then projects the tunnel ones into `tunnels` as well), so without this
  // filter every tunnel would be drawn twice -- once as an antenna, once as a lamp.
  return checks
    .filter((c) => c?.check_name && !c.check_name.startsWith('tunnel_'))
    .slice()
    .sort((a, b) => {
      const ia = LAMP_ORDER.indexOf(a.check_name)
      const ib = LAMP_ORDER.indexOf(b.check_name)
      if (ia !== ib) return (ia < 0 ? Infinity : ia) - (ib < 0 ? Infinity : ib)
      return a.check_name.localeCompare(b.check_name)
    })
}

// "ok" and "fail" are the FSM's whole vocabulary today, but a third value must not
// paint itself green: an unrecognised status is grey, not healthy. Same honesty rule
// as labels.js -- an unknown is information, and guessing "fine" is the one guess
// that gets a fault missed.
function lampTone(status) {
  if (status === 'fail') return 'alert'
  if (status === 'ok') return 'ok'
  return 'unknown'
}

// Spoken form of a lamp, for the drawing's <desc>. "не работает" is the spec's
// wording for a failed check (§3.7); an unrecognised status is echoed as-is, the
// same honesty rule labels.js applies to pingLabel/checkLabel.
function checkStateLabel(status) {
  if (status === 'fail') return 'не работает'
  if (status === 'ok') return 'работает'
  return status ?? 'неизвестно'
}

function Lamp({ cx, tone, label }) {
  const alert = tone === 'alert'
  const hue = alert
    ? 'var(--status-alert)'
    : tone === 'ok'
      ? 'var(--status-online)'
      : 'var(--status-offline)'
  return (
    <g>
      {alert ? (
        <>
          <circle class="device-bloom-alert" cx={cx} cy={LAMP_Y} r="14" fill={hue} opacity="0.3" />
          {/* The static ring and the red bezel below are what make the fault read
              when the blink is frozen by prefers-reduced-motion. */}
          <circle cx={cx} cy={LAMP_Y} r="12.6" fill="none" stroke={hue} stroke-width="1" opacity="0.4" />
          <circle cx={cx} cy={LAMP_Y} r="9.5" fill="#15181d" stroke={hue} stroke-width="1.1" />
          <circle class="device-led-alert" cx={cx} cy={LAMP_Y} r="6" fill={hue} />
        </>
      ) : (
        <>
          <circle cx={cx} cy={LAMP_Y} r="14" fill={hue} opacity="0.13" />
          <circle cx={cx} cy={LAMP_Y} r="9.5" fill="#15181d" stroke="#59616c" stroke-width="0.8" />
          <circle cx={cx} cy={LAMP_Y} r="6" fill={hue} />
        </>
      )}
      {/* Glass highlight: the lamp is a lens, not a swatch. */}
      <ellipse
        cx={cx - 2.2}
        cy="163.6"
        rx="2.3"
        ry="1.5"
        fill="#ffffff"
        opacity={alert ? '0.32' : '0.38'}
        transform={`rotate(-35 ${r2(cx - 2.2)} 163.6)`}
      />
      <text class={`device-print${alert ? ' device-print-alert' : ''}`} x={cx} y="188" text-anchor="middle">
        {label}
      </text>
    </g>
  )
}

function Antenna({ slot, tunnel, egress, waveOrigin }) {
  const { t, baseX } = slot
  // One source of truth for "is this tunnel healthy": labels.js. Re-deriving it from
  // enabled/run_state here would give the drawing and the text below it two opinions
  // -- and `enabled` is a nullable tri-state, so the naive derivation renders "we
  // never heard" as "switched off".
  const alive = tunnelStateLabel(tunnel) === 'работает'

  // Outboard droop for a dead antenna; t=0 (a lone antenna) has no outboard side, so
  // it flops right.
  const theta = t * ANT_LEAN + (alive ? 0 : rad(DROOP) * (t >= 0 ? 1 : -1))
  const sin = Math.sin(theta)
  const cos = Math.cos(theta)
  const endX = baseX + MAST_LEN * sin
  const endY = MAST_BASE_Y - MAST_LEN * cos
  const tipX = endX
  const tipY = endY - TIP_GAP
  const arcCentre = 90 - ARC_OUTBOARD * t

  const hue = alive ? 'var(--status-online)' : 'var(--status-offline)'
  // Three tiers, all of them the mockup's own: the live egress gets its full
  // radiating fan (the mockup's awg12), a healthy non-egress tunnel gets the faint
  // standing field (its awg10), and a dead one gets that same standing field in
  // grey. Only the egress radiates -- and `egress` is false unless the agent named
  // the tunnel, so an unknown routing mode leaves every antenna equally quiet
  // rather than crowning whichever one sorts first.
  const arcs = egress
    ? [
        { r: 9, w: 2, o: 0.9 },
        { r: 14, w: 1.7, o: 0.6 },
        { r: 19, w: 1.4, o: 0.32 },
      ]
    : alive
      ? [
          { r: 9, w: 1.8, o: 0.3 },
          { r: 14, w: 1.5, o: 0.17 },
        ]
      : [
          { r: 9, w: 1.8, o: 0.45 },
          { r: 14, w: 1.5, o: 0.22 },
        ]

  return (
    <g>
      {arcs.map((a) => (
        <path
          key={a.r}
          d={fieldArc(tipX, tipY, a.r, arcCentre)}
          fill="none"
          stroke={hue}
          stroke-width={a.w}
          stroke-linecap="round"
          opacity={a.o}
        />
      ))}
      {egress && (
        <path
          class="device-wave"
          style={{ transformOrigin: `${r2(tipX)}px ${r2(tipY)}px` }}
          d={fieldArc(tipX, tipY, 14, arcCentre)}
          fill="none"
          stroke={hue}
          stroke-width="1.7"
          stroke-linecap="round"
          opacity="0"
        />
      )}
      {/* The mast keeps its plastic hex in every state: a real antenna does not
          change material when a tunnel drops. Droop, field and tip carry the state. */}
      <line x1={r2(baseX)} y1={MAST_BASE_Y} x2={r2(endX)} y2={r2(endY)} stroke="#4b525c" stroke-width="7" stroke-linecap="round" />
      <line
        x1={r2(baseX - 1.8)}
        y1={MAST_BASE_Y - 1}
        x2={r2(endX - 1.8)}
        y2={r2(endY + 1)}
        stroke="#949ca9"
        stroke-width="1.6"
        stroke-linecap="round"
        opacity="0.5"
      />
      <circle cx={r2(tipX)} cy={r2(tipY)} r="3.6" fill={hue} />
      <circle cx={r2(tipX)} cy={r2(tipY)} r="6.4" fill="none" stroke={hue} stroke-width="1" opacity="0.28" />
    </g>
  )
}

function Collar({ slot, tunnel }) {
  const { baseX } = slot
  const name = tunnel.name && tunnel.name !== tunnel.tunnel_id ? tunnel.name : null
  return (
    <g>
      <ellipse cx={r2(baseX)} cy={COLLAR_Y} rx="9" ry="4.6" fill="#3a4048" stroke="#6d7480" stroke-width="0.8" />
      {/* The collar ring IS the defaultRoute indicator. Several tunnels can each
          claim it, and the drawing says so by lighting several collars -- that is
          the contested-default condition stated as a property of the object rather
          than as a footnote. It is an INTENT, never the answer to "where does
          traffic go"; the traffic path answers that. */}
      {tunnel.default_route_intent && (
        <>
          <ellipse cx={r2(baseX)} cy={COLLAR_Y} rx="12" ry="6.4" fill="var(--status-sleeping)" opacity="0.12" />
          <ellipse
            cx={r2(baseX)}
            cy={COLLAR_Y}
            rx="12"
            ry="6.4"
            fill="none"
            stroke="var(--status-sleeping)"
            stroke-width="1.6"
            opacity="0.95"
          />
        </>
      )}
      <text class="device-print-port" x={r2(baseX)} y="132" text-anchor="middle">
        {tunnel.tunnel_id}
      </text>
      {name && (
        <text class="device-print-iface" x={r2(baseX)} y="141.5" text-anchor="middle">
          {name}
        </text>
      )}
    </g>
  )
}

// The traffic path -- the one element the mockup never had, and the reason this
// screen exists. It answers "where do my packets actually leave", and it is drawn
// last so it reads over the hardware.
function TrafficPath({ mode, egressSlot, laneX, arrow }) {
  const casing = (d, key) => <path key={key} class="device-path-casing" d={d} />

  // Up and out through the egress antenna's own axis, offset inboard so the mast
  // stays visible underneath rather than being painted over.
  function vpnPath(slot) {
    const { t, baseX } = slot
    const theta = t * ANT_LEAN
    const u = [Math.sin(theta), -Math.cos(theta)]
    const n = [Math.cos(theta), Math.sin(theta)]
    const off = t >= 0 ? -9.5 : 9.5
    const tipD = MAST_LEN + TIP_GAP + 26
    const ex = baseX + u[0] * tipD + n[0] * off
    const ey = MAST_BASE_Y + u[1] * tipD + n[1] * off
    // Leaves the body inboard of the collar, then swings onto the antenna's line.
    const sx = baseX - (t >= 0 ? 26 : -26)
    const c1x = sx
    const c1y = TOP - 30
    const c2x = ex - u[0] * 36
    const c2y = ey - u[1] * 36
    return `M ${r2(sx)} ${TOP} C ${r2(c1x)} ${r2(c1y)}, ${r2(c2x)} ${r2(c2y)}, ${r2(ex)} ${r2(ey)}`
  }

  const wanD = `M 341 ${WAN.cy} L 366 ${WAN.cy}`
  const upD = `M ${r2(laneX)} ${TOP} L ${r2(laneX)} 62`

  if (mode === 'vpn' && egressSlot) {
    const d = vpnPath(egressSlot)
    return (
      <g>
        {casing(d, 'v')}
        <path class="device-path" d={d} marker-end={arrow} />
      </g>
    )
  }

  if (mode === 'direct') {
    return (
      <g>
        {casing(wanD, 'w')}
        <path class="device-path" d={wanD} marker-end={arrow} />
      </g>
    )
  }

  if (mode === 'singbox') {
    // Forked: sing-box picks a route per destination, so both exits are live and
    // NEITHER names a tunnel -- the up branch rises in the free lane between the
    // masts, because "some traffic goes through a tunnel" is all we can honestly say.
    return (
      <g>
        {casing(upD, 'u')}
        <path class="device-path" d={upD} marker-end={arrow} />
        {casing(wanD, 'w')}
        <path class="device-path" d={wanD} marker-end={arrow} />
      </g>
    )
  }

  // unknown: a dashed stub that leads nowhere, with no arrowhead and no tunnel at
  // the end of it. An agent older than this phase genuinely cannot say which tunnel
  // is the egress; drawing a confident line at one of them would be the exact lie
  // this whole phase exists to prevent.
  const unkD = `M ${r2(laneX)} ${TOP} L ${r2(laneX)} 76`
  return (
    <g>
      {casing(unkD, 'q')}
      <path class="device-path device-path-unknown" d={unkD} />
      <text class="device-qmark" x={r2(laneX)} y="66" text-anchor="middle">
        ?
      </text>
    </g>
  )
}

export function RouterDevice({ tunnels = [], traffic, checks = [], name }) {
  const uid = useId()
  const lamps = orderChecks(checks)
  const slots = antennaSlots(tunnels.length)
  const laneX = freeLaneX(slots.map((s) => s.baseX))

  // Field names verified against miniappTunnel/miniappTraffic's json tags
  // (internal/backend/miniapp_tunnels.go:28-54, 123-132): tunnel_id, name,
  // default_route_intent, mode, egress_tunnel_id.
  const egressIdx =
    traffic?.mode === 'vpn' && traffic.egress_tunnel_id
      ? tunnels.findIndex((t) => t.tunnel_id === traffic.egress_tunnel_id)
      : -1
  // A "vpn" verdict naming a tunnel we were not given is not a tunnel we may point
  // at. Fall back to the unknown treatment rather than to a guess.
  const mode = traffic?.mode === 'vpn' && egressIdx < 0 ? 'unknown' : (traffic?.mode ?? 'unknown')

  const lampPitch = lamps.length > 1 ? (LAMP_LAST - LAMP_FIRST) / (lamps.length - 1) : 0
  const lampX = (i) => (lamps.length === 1 ? CENTER : LAMP_FIRST + i * lampPitch)

  // The nameplate yields to the hardware: at an odd tunnel count an antenna stands
  // on the centre line and its port silkscreen would collide with the etch.
  const etchClear = name && slots.every((s) => Math.abs(s.baseX - CENTER) >= 46)

  // The description states what is DRAWN, so it reads `mode` (already degraded to
  // "unknown" if the egress names a tunnel we don't have) rather than the raw
  // traffic.mode -- a screen reader announcing "traffic goes via VPN" over a drawing
  // that shows a question mark would be the same lie, just out loud.
  const desc = [
    lamps.length
      ? `Индикаторы: ${lamps
          .map((c) => `${lampKey(c.check_name)} ${checkLabel(c.check_name)} — ${checkStateLabel(c.status)}`)
          .join('; ')}.`
      : 'Индикаторов нет.',
    tunnels.length
      ? `Антенны: ${tunnels.map((t) => `${t.name || t.tunnel_id} — ${tunnelStateLabel(t)}`).join('; ')}.`
      : 'Антенн нет: туннели не заведены.',
    trafficLabel({ ...traffic, mode }).title + '.',
  ].join(' ')

  return (
    <div class="device-stage">
      <svg
        class="device"
        viewBox="-4 -14 392 262"
        role="img"
        aria-labelledby={`${uid}-t ${uid}-d`}
        preserveAspectRatio="xMidYMid meet"
      >
        <title id={`${uid}-t`}>{name ? `Передняя панель роутера ${name}` : 'Передняя панель роутера'}</title>
        <desc id={`${uid}-d`}>{desc}</desc>

        <defs>
          <linearGradient id={`${uid}-sheen`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stop-color="#ffffff" stop-opacity="0.075" />
            <stop offset="0.5" stop-color="#ffffff" stop-opacity="0" />
          </linearGradient>
          <pattern id={`${uid}-vents`} x="44" y="206" width="8" height="18" patternUnits="userSpaceOnUse">
            <rect x="0" y="0" width="3" height="18" rx="1.5" fill="#2a2f36" />
          </pattern>
          <clipPath id={`${uid}-panel`}>
            <rect x={PANEL.x} y={PANEL.y} width={PANEL.w} height={PANEL.h} rx={PANEL.rx} />
          </clipPath>
          <marker
            id={`${uid}-arrow`}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
            markerUnits="strokeWidth"
          >
            <path d="M 0 1 L 9 5 L 0 9 z" fill="var(--accent-ink)" />
          </marker>
        </defs>

        {/* Contact shadow. Reads on light surfaces; on near-black it simply vanishes
            and the rim highlight on the shell carries the silhouette. */}
        <ellipse cx="174" cy="243" rx="118" ry="4.5" fill="#000000" opacity="0.16" />

        {/* Antennas go under the shell so the body covers the masts' feet. */}
        {slots.map((slot, i) => (
          <Antenna key={tunnels[i].tunnel_id ?? i} slot={slot} tunnel={tunnels[i]} egress={i === egressIdx} />
        ))}

        {/* ================= SHELL ================= */}
        <rect x="44" y="233" width="40" height="6" rx="2" fill="#2d323a" />
        <rect x="264" y="233" width="40" height="6" rx="2" fill="#2d323a" />

        {/* WAN port: a side connector, always present, lit only by the traffic path. */}
        <rect x={WAN.x} y={WAN.y} width={WAN.w} height={WAN.h} rx="2" fill="#2d323a" stroke="#5c646f" stroke-width="0.6" />

        <rect x={SHELL.x} y={SHELL.y} width={SHELL.w} height={SHELL.h} rx={SHELL.rx} fill="#414852" stroke="#79808c" stroke-width="1.3" />
        <rect x="18.65" y="108.65" width="310.7" height="124.7" rx="13.4" fill={`url(#${uid}-sheen)`} />
        <path d="M 32 232.4 H 316" stroke="#2c3138" stroke-width="1.2" opacity="0.85" />

        <g fill="#363c45" stroke="#5c646f" stroke-width="0.6">
          <circle cx="28.5" cy="120" r="2.3" />
          <circle cx="319.5" cy="120" r="2.3" />
          <circle cx="28.5" cy="222" r="2.3" />
          <circle cx="319.5" cy="222" r="2.3" />
        </g>

        {slots.map((slot, i) => (
          <Collar key={tunnels[i].tunnel_id ?? i} slot={slot} tunnel={tunnels[i]} />
        ))}

        {etchClear && (
          <text class="device-etch" x={CENTER} y="132" text-anchor="middle">
            {name}
          </text>
        )}

        {/* ================= INDICATOR PANEL ================= */}
        <rect x={PANEL.x} y={PANEL.y} width={PANEL.w} height={PANEL.h} rx={PANEL.rx} fill="#252a30" stroke="#575f6a" stroke-width="0.8" />
        {/* A failing cell runs hot: its recess is washed red. */}
        {lamps.map((c, i) =>
          c.status === 'fail' ? (
            <rect
              key={c.check_name}
              x={r2(lampX(i) - (lampPitch || 47.2) / 2)}
              y="148.6"
              width={r2((lampPitch || 47.2) - 1.2)}
              height="46.8"
              fill="var(--status-alert)"
              opacity="0.07"
              clip-path={`url(#${uid}-panel)`}
            />
          ) : null,
        )}

        {lamps.map((c, i) => (
          <Lamp key={c.check_name} cx={r2(lampX(i))} tone={lampTone(c.status)} label={lampKey(c.check_name)} />
        ))}

        <rect x="44" y="206" width="260" height="18" fill={`url(#${uid}-vents)`} />
        <text class="device-print" x="317" y="216" text-anchor="middle">
          WAN
        </text>

        <TrafficPath mode={mode} egressSlot={slots[egressIdx]} laneX={laneX} arrow={`url(#${uid}-arrow)`} />
      </svg>
    </div>
  )
}
