// Нижняя навигация. Четыре таба -- четыре вопроса оператора: что с роутером,
// куда идёт трафик, что показывает диагностика, что происходило раньше.
const ICONS = {
  router: (
    <svg viewBox="0 0 22 22" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true">
      <rect x="2.5" y="7" width="17" height="9" rx="2.5" />
      <path d="M 6 11.5 h 0.01 M 9.5 11.5 h 0.01 M 13 11.5 h 0.01" />
      <path d="M 6 7 L 4 3.5 M 16 7 L 18 3.5" />
    </svg>
  ),
  tunnels: (
    <svg viewBox="0 0 22 22" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M 3 5.5 h 6 c 3 0 3 11 6 11 h 4" />
      <path d="M 3 16.5 h 5" />
      <path d="M 16 13.5 L 19 16.5 L 16 19.5" />
    </svg>
  ),
  diag: (
    <svg viewBox="0 0 22 22" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M 2.5 11 h 4 l 2.5 -6 l 3.5 12 l 2.5 -6 h 4.5" />
    </svg>
  ),
  events: (
    <svg viewBox="0 0 22 22" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M 11 3.5 a 5.5 5.5 0 0 1 5.5 5.5 v 4 l 1.5 3 h -14 l 1.5 -3 v -4 A 5.5 5.5 0 0 1 11 3.5 z" />
      <path d="M 9 18.5 a 2 2 0 0 0 4 0" />
    </svg>
  ),
}

const LABELS = {
  router: 'Роутер',
  tunnels: 'Туннели',
  diag: 'Диагностика',
  events: 'События',
}

export function TabBar({ tab, onTab, tabs }) {
  return (
    <nav class="tabbar">
      {tabs.map((key) => (
        <button
          key={key}
          type="button"
          class={`tabbar-item${key === tab ? ' tabbar-item-active' : ''}`}
          aria-current={key === tab ? 'page' : undefined}
          onClick={() => onTab(key)}
        >
          {ICONS[key]}
          {LABELS[key]}
        </button>
      ))}
    </nav>
  )
}
