// Бэкенд не ответил при старте веб-управления. В Telegram-режиме прежний
// текст «откройте из Telegram заново»; здесь он был бы неправдой.
export function ServerDown({ onRetry }) {
  return (
    <div class="login">
      <div class="login-card">
        <span class="login-brand">wg-monitor</span>
        <p class="state state-error">Сервер не отвечает</p>
        <p class="login-lead">Проверьте связь с сервером и повторите.</p>
        <button type="button" class="btn btn-primary btn-wide" onClick={onRetry}>
          Повторить
        </button>
      </div>
    </div>
  )
}
