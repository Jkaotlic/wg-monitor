// Пилюля -- короткое показание рядом с именем. tone -- смысл, а не цвет.
export function Pill({ tone = 'muted', children }) {
  const cls = tone === 'muted' ? 'pill' : `pill pill-${tone}`
  return <span class={cls}>{children}</span>
}
