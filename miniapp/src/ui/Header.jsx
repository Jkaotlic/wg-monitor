// Шапка приложения. Слева -- имя открытого роутера кнопкой «Дача ▾» (v0.50,
// спека п. 2.6): лист быстрого выбора, вкладка при смене остаётся. Без
// выбора (один роутер у владельца, главный экран-список, вкладка «Парк») --
// бренд. Справа «Все роутеры» -- когда роутеров больше одного или у админа.
//
// «Выйти» -- только в веб-управлении (onLogout передаёт оболочка web).
export function Header({ fleetVisible, onFleet, onLogout, router = null, onSwitch }) {
  return (
    <div class="app-header">
      {router && onSwitch ? (
        <button type="button" class="router-switch" aria-haspopup="dialog" title={router.nickname} onClick={onSwitch}>
          <span class="router-switch-name">{router.nickname}</span>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M4 6l4 4 4-4" />
          </svg>
        </button>
      ) : (
        <span class="app-header-brand">wg-monitor</span>
      )}
      {fleetVisible && (
        <button type="button" class="app-header-fleet" onClick={onFleet}>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true">
            <rect x="2" y="2.5" width="12" height="4" rx="1.2" />
            <rect x="2" y="9.5" width="12" height="4" rx="1.2" />
          </svg>
          Все роутеры
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
