// Пилюля -- короткое показание рядом с именем. tone -- смысл, а не цвет.
// title -- необязательный tooltip/long-press текст: нужен там, где сама
// пилюля обрезается по ширине колонки (v0.49, ярлык роутера у пира).
export function Pill({ tone = 'muted', title, children }) {
  const cls = tone === 'muted' ? 'pill' : `pill pill-${tone}`
  return (
    <span class={cls} title={title}>
      {children}
    </span>
  )
}
