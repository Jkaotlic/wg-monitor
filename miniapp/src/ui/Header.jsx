// Шапка (v0.52, спека §3): выбор роутера -- один вход, имя роутера. С выбором
// (6+ роутеров, админ) имя -- кнопка «▾»; без выбора (один роутер, 2–5 --
// там выбирает полоса под шапкой) -- просто имя. «Все роутеры» больше нет.
// «Выйти» -- только в веб-управлении.
export function Header({ title, onPick, onLogout }) {
  return (
    <div class="app-header">
      {onPick ? (
        <button type="button" class="router-switch" aria-haspopup="dialog" aria-label={`Выбрать роутер: ${title}`} title={title} onClick={onPick}>
          <span class="router-switch-name">{title}</span>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M4 6l4 4 4-4" />
          </svg>
        </button>
      ) : (
        <span class="app-header-title" title={title}>{title}</span>
      )}
      {onLogout && (
        <button type="button" class="app-header-fleet app-header-logout" onClick={onLogout}>
          Выйти
        </button>
      )}
    </div>
  )
}
