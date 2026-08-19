// Статусная пилюля. tone -- смысл, а не цвет: ok/warn/danger/muted.
// Точка внутри рисуется правилом .badge::before цветом currentColor.
export function Chip({ tone = 'muted', children }) {
  return <span class={`badge badge-tone-${tone}`}>{children}</span>
}
