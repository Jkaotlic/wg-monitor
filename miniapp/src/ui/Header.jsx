import { GearIcon } from './GearIcon.jsx'

// Шапка приложения. Кнопка "Мои роутеры" появляется только когда роутеров
// больше одного: человеку с единственным роутером список показывать незачем.
//
// Шестерёнка ведёт в настройки роутера, а не приложения: настраивать в самом
// мини-аппе нечего, а пороги, версии и обслуживание -- ровно то, за чем
// человек раньше шёл в бота.
//
// «Выйти» -- только в веб-управлении (onLogout передаёт оболочка web): в
// Telegram выходить не из чего, личность там -- сам Telegram.
export function Header({ fleetVisible, onFleet, onSettings, onLogout }) {
  return (
    <div class="app-header">
      <span class="app-header-brand">wg-monitor</span>
      {onSettings && (
        <button type="button" class="app-header-gear" onClick={onSettings} aria-label="Настройки">
          <GearIcon />
        </button>
      )}
      {fleetVisible && (
        <button type="button" class="app-header-fleet" onClick={onFleet}>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true">
            <rect x="2" y="2.5" width="12" height="4" rx="1.2" />
            <rect x="2" y="9.5" width="12" height="4" rx="1.2" />
          </svg>
          Мои роутеры
        </button>
      )}
      {onLogout && (
        <button type="button" class="app-header-fleet app-header-logout" onClick={onLogout}>
          Выйти
        </button>
      )}
    </div>
  )
}
