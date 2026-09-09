import { pathState } from '../trafficPath.js'

// Схема пути трафика: устройства → роутер → развилка.
//
// Заменяет прибор с портами DNS/EXT/HR/AWGM/AGEN. Тот рисовал внутренние
// проверки как разъёмы железки и требовал легенду под собой — если картинке
// нужна инструкция, она не работает. Эта показывает то, ради чего сервис
// существует: заблокированное идёт через туннель, остальное напрямую.
//
// Ветки живут порознь. Упавший туннель рвётся пунктиром, а прямая ветка
// остаётся целой — человек видит, что банки и госуслуги у него работают, и не
// решает, что интернет пропал целиком.
const TONE = {
  up: { stroke: 'var(--sig)', fill: 'rgba(200,242,74,0.06)', border: 'rgba(200,242,74,0.3)', label: 'var(--sig)' },
  down: { stroke: 'var(--bad)', fill: 'rgba(255,106,88,0.07)', border: 'rgba(255,106,88,0.35)', label: 'var(--bad)' },
  unknown: { stroke: 'rgba(255,255,255,0.2)', fill: 'var(--surf)', border: 'var(--line)', label: 'var(--dim)' },
}

const TUNNEL_CAPTION = {
  up: 'то, что заблокировано',
  down: 'не отвечает',
  unknown: 'роутер не сказал',
}

export function TrafficPath({ traffic, incidents, tunnels, stale }) {
  const s = pathState({ traffic, incidents, tunnels, stale })
  const t = TONE[s.tunnel]
  const d = TONE[s.direct]
  const viaLabel = s.via || 'линия'
  const latency = s.latencyMs != null ? `${s.latencyMs} мс` : ''

  return (
    <svg
      viewBox="0 0 342 232"
      width="100%"
      class="traffic-path"
      role="img"
      aria-label={`Схема: заблокированное идёт через «${viaLabel}», остальное напрямую`}
    >
      <rect x="103" y="2" width="136" height="42" rx="12" fill="var(--surf2)" stroke="var(--line)" />
      <g stroke="var(--dim)" stroke-width="1.4" fill="none" stroke-linecap="round">
        <rect x="118" y="15" width="10" height="15" rx="2" />
        <rect x="134" y="17" width="15" height="11" rx="1.6" />
        <path d="M132 30h19" />
        <circle cx="161" cy="22.5" r="6" />
      </g>
      <text x="176" y="27" fill="var(--dim)" font-size="12">Ваши устройства</text>

      <path d="M171 44v22" stroke="rgba(255,255,255,0.18)" stroke-width="1.5" />

      <rect x="115" y="66" width="112" height="40" rx="12" fill="var(--surf2)" stroke={t.border} />
      <circle cx="133" cy="86" r="4" fill={stale ? 'var(--dim)' : 'var(--ok)'} />
      <text x="145" y="91" fill="var(--ink)" font-size="13" font-weight="600">Роутер</text>

      {/* Ветка обхода: рвётся пунктиром, когда линия не отвечает. */}
      <path
        d="M171 106v14c0 8-7 10-14 10H96c-8 0-12 4-12 12v10"
        stroke={t.stroke}
        stroke-width="1.8"
        fill="none"
        stroke-dasharray={s.tunnel === 'down' ? '5 5' : undefined}
      />
      <path
        d="M171 106v14c0 8 7 10 14 10h61c8 0 12 4 12 12v10"
        stroke={d.stroke}
        stroke-width="1.8"
        fill="none"
      />

      <rect x="2" y="152" width="164" height="78" rx="14" fill={t.fill} stroke={t.border} />
      <text x="16" y="174" fill={t.label} font-family="var(--font-mono)" font-size="10" letter-spacing="0.06em">
        {s.tunnel === 'down' ? 'ТУННЕЛЬ МОЛЧИТ' : 'ЧЕРЕЗ ТУННЕЛЬ'}
      </text>
      <text x="16" y="195" fill="var(--ink)" font-size="14" font-weight="600">{viaLabel}</text>
      <text x="16" y="215" fill={s.tunnel === 'down' ? t.label : 'var(--dim)'} font-size="11.5">
        {latency && s.tunnel === 'up' ? `${TUNNEL_CAPTION.up} · ${latency}` : TUNNEL_CAPTION[s.tunnel]}
      </text>

      <rect x="176" y="152" width="164" height="78" rx="14" fill="var(--surf)" stroke="var(--line)" />
      <text x="190" y="174" fill={stale ? 'var(--dim)' : 'var(--ok)'} font-family="var(--font-mono)" font-size="10" letter-spacing="0.06em">
        {stale ? 'НЕИЗВЕСТНО' : 'НАПРЯМУЮ'}
      </text>
      <text x="190" y="195" fill="var(--ink)" font-size="14" font-weight="600">Без туннеля</text>
      <text x="190" y="215" fill="var(--dim)" font-size="11.5">банки, госуслуги, ТВ</text>
    </svg>
  )
}
