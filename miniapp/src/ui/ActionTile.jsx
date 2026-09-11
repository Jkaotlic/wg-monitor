import { Quoted } from './Q.jsx'

// Плитка быстрого действия: крупная подпись и мелкое пояснение под ней --
// что именно произойдёт и сколько это займёт. Имена в них -- через <Quoted>.
export function ActionTile({ title, hint, onClick, danger = false, disabled = false }) {
  return (
    <button
      type="button"
      class={`action-tile${danger ? ' action-tile-danger' : ''}`}
      onClick={onClick}
      disabled={disabled}
    >
      <span class="action-tile-title">
        <Quoted text={title} />
      </span>
      {hint && (
        <span class="action-tile-hint">
          <Quoted text={hint} />
        </span>
      )}
    </button>
  )
}
