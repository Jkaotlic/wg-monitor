// Оверлей -- слой поверх табов: список роутеров, администрирование.
// Своя кнопка "назад" дублирует телеграмовскую сознательно: на десктопе
// системной кнопки может не быть вовсе.
export function Overlay({ title, backLabel = 'Назад', onBack, children }) {
  return (
    <div class="overlay">
      <div class="overlay-head">
        <button type="button" class="overlay-back" onClick={onBack}>
          <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M 10 3 L 5 8 L 10 13" />
          </svg>
          {backLabel}
        </button>
        <span class="overlay-title">{title}</span>
      </div>
      <div class="overlay-body">{children}</div>
    </div>
  )
}
