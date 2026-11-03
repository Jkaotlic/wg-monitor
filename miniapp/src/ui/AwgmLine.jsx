import { awgmToneClass } from '../awgmBadge.js'

// Строка «awg-manager: <слово>» под именем VPN-туннеля (v0.65): слово самого
// роутера рядом с вердиктом бота, чтобы расхождение было видно сразу. Точка и
// слово -- существующими классами тона экрана. Нет слова -- нет строки.
export function AwgmLine({ badge }) {
  if (!badge) return null
  const tone = awgmToneClass(badge.tone)
  return (
    <span class="awgm-line">
      <span class={`data-row-dot data-row-dot-${tone}`} />
      <span>
        awg-manager: <span class={`data-row-value-${tone}`}>{badge.label}</span>
        {badge.note ? ` · ${badge.note}` : ''}
      </span>
    </span>
  )
}
