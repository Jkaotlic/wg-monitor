// Плитка-счётчик. unknown -- отдельное состояние, а не пустая строка: цифры
// у нас нет, и набирать её отсутствие тем же кеглем, что живое показание,
// значило бы уравнять "26" и "неизвестно".
export function Stat({ label, value, unit, note, tone }) {
  const known = value != null && value !== ''
  const cls = ['stat', !known ? 'stat-unknown' : tone ? `stat-${tone}` : ''].filter(Boolean).join(' ')
  return (
    <div class={cls}>
      <span class="stat-label">{label}</span>
      <span class="stat-value">
        {known ? value : 'неизвестно'}
        {known && unit ? <span class="stat-unit">{unit}</span> : null}
      </span>
      {note ? <span class="stat-note">{note}</span> : null}
    </div>
  )
}
