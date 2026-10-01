import { barLabel } from '../nav.js'
import { GearIcon } from './GearIcon.jsx'

// Нижняя навигация (v0.52): четыре вкладки по задаче человека -- «Роутер»,
// «VPN-туннели», «Проверки», «Настройки»; админу первой добавляется «Парк».
// Списка роутеров в панели нет: выбор роутера -- в шапке или полосе.
// Значки вкладок -- общие с колонкой широкой раскладки (Sidebar).
export const TAB_ICONS = {
  park: (
    <svg viewBox="0 0 22 22" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <rect x="2.5" y="3" width="7" height="7" rx="1.6" />
      <rect x="12.5" y="3" width="7" height="7" rx="1.6" />
      <rect x="2.5" y="12" width="7" height="7" rx="1.6" />
      <rect x="12.5" y="12" width="7" height="7" rx="1.6" />
    </svg>
  ),
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
  manage: <GearIcon size={22} />,
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
          {TAB_ICONS[key]}
          {barLabel(key)}
        </button>
      ))}
    </nav>
  )
}
