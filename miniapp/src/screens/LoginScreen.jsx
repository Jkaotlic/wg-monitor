import { useEffect, useRef, useState } from 'preact/hooks'
import { dashboardLogin, redeemWebLink } from '../api.js'
import { loginErrorText, redeemFromHash } from '../login.js'

// Вход в веб-управление. Два пути: личная ссылка из мини-аппа (токен в
// хэше, обмен без участия человека) и токен доступа руками. Ссылка, которая
// не сработала, не оставляет человека в тупике: под ошибкой та же форма.
export function LoginScreen({ notice = '', onSuccess }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [redeeming, setRedeeming] = useState(false)
  const [error, setError] = useState('')
  const alive = useRef(true)

  useEffect(() => {
    alive.current = true
    const pending = redeemFromHash({ location: window.location, history: window.history, redeem: redeemWebLink })
    if (pending) {
      setRedeeming(true)
      pending
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
      </form>
    </div>
  )
}
