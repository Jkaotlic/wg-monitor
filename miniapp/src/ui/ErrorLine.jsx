// Ошибка действия и, если его можно просто повторить, -- «Повторить» рядом
// (v0.50, спека п. 1.4). Текст -- уже фраза для человека (errorText).
export function ErrorLine({ text, onRetry, busy = false, retryLabel = 'Повторить' }) {
  if (!text) return null
  return (
    <div class="error-line" role="alert">
      <p class="state state-error">{text}</p>
      {onRetry && (
        <button type="button" class="btn btn-ghost error-line-retry" disabled={busy} onClick={onRetry}>
          {retryLabel}
        </button>
      )}
    </div>
  )
}
