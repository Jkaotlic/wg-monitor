// Плитка быстрого действия: крупная подпись и мелкое пояснение под ней --
// что именно произойдёт и сколько это займёт.
export function ActionTile({ title, hint, onClick, danger = false, disabled = false }) {
  return (
    <button
      type="button"
      class={`action-tile${danger ? ' action-tile-danger' : ''}`}
      onClick={onClick}
      disabled={disabled}
    >
      <span class="action-tile-title">{title}</span>
      {hint && <span class="action-tile-hint">{hint}</span>}
    </button>
  )
}
