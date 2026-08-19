// Метка состояния: светящаяся точка и подпись капслоком. Это то, что видно
// боковым зрением, когда экран открывают на ходу, -- поэтому тон здесь
// значит состояние линии, а не настроение.
export function StateTag({ tone = 'sig', children }) {
  const cls = tone === 'sig' ? 'state-tag' : `state-tag state-tag-${tone}`
  return <span class={cls}>{children}</span>
}
