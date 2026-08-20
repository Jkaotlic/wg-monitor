// Порядок подхвата: линия связывает звенья, потому что это очередь, а не
// список. Первое и последнее звено линию обрезают -- иначе она обещает
// продолжение, которого нет.
const DOT = { active: 'chain-dot chain-dot-active', ready: 'chain-dot chain-dot-ready', off: 'chain-dot' }

export function Chain({ links }) {
  return (
    <div class="chain">
      {links.map((l) => (
        <div key={l.bind ?? l.tunnelID ?? l.name} class="chain-link">
          <span class={DOT[l.role] ?? DOT.off} />
          <span class="chain-main">
            {l.title ?? l.note}
            {l.name ? <u class="data-row-code">{l.name}</u> : null}
          </span>
          <span class="chain-note">{l.value ?? ''}</span>
          {/* Действие звена -- необязательный слот: у цепочки, которую
              нельзя трогать, его просто нет. */}
          {l.action ?? null}
        </div>
      ))}
    </div>
  )
}
