// Полоса «Мои роутеры» (v0.52, спека §4): 2–5 роутеров -- чипы «имя + точка»
// под шапкой на всех вкладках роутера. Тревога первой и окрашена; полоса
// прокручивается сама, страница -- нет. Состояние словами -- в aria-label.
export function RouterStrip({ chips = [], onPick }) {
  return (
    <nav class="router-strip" aria-label="Мои роутеры">
      {chips.map((c) => (
        <button
          key={c.id}
          type="button"
          class={`strip-chip${c.current ? ' strip-chip-current' : ''}${c.alert ? ' strip-chip-alert' : ''}`}
          aria-current={c.current ? 'page' : undefined}
          aria-label={`${c.name}: ${c.state}`}
          onClick={() => !c.current && onPick(c.id)}
        >
          <span class={`strip-dot strip-dot-${c.tone}`} aria-hidden="true" />
          <span class="strip-name">{c.name}</span>
        </button>
      ))}
    </nav>
  )
}
