// Шапка приложения. Кнопка "Мои роутеры" появляется только когда роутеров
// больше одного: человеку с единственным роутером список показывать незачем.
export function Header({ fleetVisible, onFleet }) {
  return (
    <div class="app-header">
      <span class="app-header-brand">wg-monitor</span>
      {fleetVisible && (
        <button type="button" class="app-header-fleet" onClick={onFleet}>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true">
            <rect x="2" y="2.5" width="12" height="4" rx="1.2" />
            <rect x="2" y="9.5" width="12" height="4" rx="1.2" />
          </svg>
          Мои роутеры
        </button>
      )}
    </div>
  )
}
