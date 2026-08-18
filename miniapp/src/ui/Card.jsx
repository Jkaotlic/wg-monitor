export function Card({ children, class: cls = '' }) {
  return <div class={`card ${cls}`.trim()}>{children}</div>
}
