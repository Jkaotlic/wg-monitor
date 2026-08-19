// Переход на соседний экран. Единственное место, кроме главного действия, где
// сигнальный лайм допустим: это и есть "иди сюда".
export function NavCard({ title, note, onClick }) {
  return (
    <button type="button" class="nav-card" onClick={onClick}>
      <span class="nav-card-title">{title}</span>
      {note ? <span class="nav-card-note">{note}</span> : null}
      <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M6 3l5 5-5 5" />
      </svg>
    </button>
  )
}
