import { useRef, useState } from 'preact/hooks'
import { createSession, dashboardLogout, fetchRouters, fetchSession } from './api.js'
import { getInitData } from './telegram.js'
import { SESSION_EXPIRED_TEXT, bootFailure } from './login.js'

// Вход и список роутеров -- одним шагом: без списка нельзя решить, открыть ли
// роутер, показать список или экран пустого доступа. В Telegram личность
// доказывает initData, в браузере -- кука веб-управления (GET /session).
export function useBoot(mode, { onReady } = {}) {
  const [state, setState] = useState({ status: 'loading', isAdmin: false, routers: [], notice: '' })
  const readyRef = useRef(onReady)
  readyRef.current = onReady

  function start() {
    setState((prev) => ({ ...prev, status: 'loading' }))
    const session = mode === 'web' ? fetchSession() : createSession(getInitData())
    return session
      .then((s) => fetchRouters().then((data) => ({ s, list: data?.routers ?? [] })))
      .then(({ s, list }) => {
        // До setState: навигация из адреса и статус «готово» -- один рендер,
        // иначе синхронизация адреса успела бы стереть место пустой навигацией.
        readyRef.current?.(list)
        setState({ status: 'ready', isAdmin: Boolean(s?.is_admin), routers: list, notice: '' })
      })
      .catch((err) => setState((prev) => ({ ...prev, ...bootFailure(err, mode) })))
  }

  function expire() {
    setState((prev) => ({ ...prev, status: 'login', notice: SESSION_EXPIRED_TEXT }))
  }

  // Выход не ждёт удачи запроса: человек нажал «Выйти» и должен оказаться на
  // экране входа, даже если сеть моргнула.
  function logout() {
    return dashboardLogout()
      .catch(() => null)
      .then(() => setState({ status: 'login', isAdmin: false, routers: [], notice: '' }))
  }

  function setRouters(list) {
    setState((prev) => ({ ...prev, routers: list ?? [] }))
  }

  function refreshRouters() {
    return fetchRouters()
      .then((data) => setRouters(data?.routers ?? []))
      .catch(() => {})
  }

  return { ...state, start, expire, logout, setRouters, refreshRouters }
}
