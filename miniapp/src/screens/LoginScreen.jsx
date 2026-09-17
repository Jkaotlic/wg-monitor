import { useEffect, useRef, useState } from 'preact/hooks'
import { dashboardLogin, redeemWebLink } from '../api.js'
import { loginErrorText } from '../login.js'

// Вход в веб-управление. Два пути: личная ссылка из мини-аппа (токен в
// хэше, обмен без участия человека) и токен доступа руками. Ссылка, которая
// не сработала, не оставляет человека в тупике: под ошибкой та же форма.
export function LoginScreen({ notice = '', linkToken = '', onLinkUsed, onSuccess }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [redeeming, setRedeeming] = useState(false)
  const [error, setError] = useState('')
  const alive = useRef(true)

  // Токен ссылки оболочка сняла с адреса ещё до первого запроса и держит в
  // памяти. Обмен -- один раз: onLinkUsed забывает токен, чтобы после выхода
  // или истечения сессии экран входа не менял мёртвую ссылку снова.
  useEffect(() => {
    alive.current = true
    if (linkToken) {
      onLinkUsed?.()
      setRedeeming(true)
      redeemWebLink(linkToken)
        .then(() => {
          if (alive.current) onSuccess()
        })
        .catch((err) => {
          if (alive.current) setError(loginErrorText(err, 'link'))
        })
        .finally(() => {
          if (alive.current) setRedeeming(false)
        })
    }
    return () => {
      alive.current = false
    }
  }, [])

  function submit(e) {
    e.preventDefault()
    const value = token.trim()
    if (!value || busy) return
    setBusy(true)
    setError('')
    dashboardLogin(value)
      .then(() => {
        if (alive.current) onSuccess()
      })
      .catch((err) => {
        // Поле не очищается: человек исправит одну букву, а не наберёт заново.
        if (alive.current) setError(loginErrorText(err, 'token'))
      })
      .finally(() => {
        if (alive.current) setBusy(false)
      })
  }

  return (
    <div class="login">
      <form class="login-card" onSubmit={submit} noValidate>
        <span class="login-brand">wg-monitor</span>
        <h1 class="login-title">Веб-управление</h1>
        <p class="login-lead">
          Вход для администратора: токен доступа или личная ссылка из мини-аппа
          (Парк → «Открыть в браузере»).
        </p>
        {notice && <p class="login-notice">{notice}</p>}
        {redeeming ? (
          <p class="state">Проверяем ссылку…</p>
        ) : (
          <>
            <div class="field">
              <label for="login-token">Токен доступа</label>
              <input
                id="login-token"
                type="password"
                autocomplete="current-password"
                autocapitalize="off"
                spellcheck={false}
                value={token}
                onInput={(e) => setToken(e.currentTarget.value)}
              />
            </div>
            {error && (
              <p class="state state-error login-error" role="alert">
                {error}
              </p>
            )}
            <button type="submit" class="btn btn-primary btn-wide" disabled={busy || !token.trim()}>
              {busy ? 'Входим…' : 'Войти'}
            </button>
          </>
        )}
        {/* Если само приложение не грузится, сюда человек не дойдёт; ссылка
            нужна тому, кто дошёл, но дальше экран ломается. Страница
            аварийная: без бандла, вход и раскатка бэкенда. */}
        <p class="login-rescue">
          <a href="/dashboard/rescue/">Приложение не работает? Аварийная страница</a>
        </p>
      </form>
    </div>
  )
}
