import { useRef, useState } from 'preact/hooks'
import { createSession, dashboardLogout, fetchRouters, fetchSession } from './api.js'
import { getInitData } from './telegram.js'
import { COOKIE_NOT_SAVED_TEXT, SESSION_EXPIRED_TEXT, bootFailure } from './login.js'

// Вход и список роутеров -- одним шагом: без списка нельзя решить, открыть ли
// роутер, показать список или экран пустого доступа. В Telegram личность
// доказывает initData, в браузере -- кука веб-управления (GET /session).
export function useBoot(mode, { onReady } = {}) {
  const [state, setState] = useState({ status: 'loading', isAdmin: false, telegramUserID: 0, routers: [], notice: '' })
  const readyRef = useRef(onReady)
  readyRef.current = onReady

  // Поколение сессии: растёт при каждом входе, выходе и истечении. Ответ,
  // запрошенный в прошлом поколении (обновление списка до «Выйти»), к
  // состоянию уже не относится и молча выбрасывается.
  const gen = useRef(0)

  // afterLogin -- старт сразу после удачного входа или обмена ссылки. Если и
  // тогда сессии нет, кука не легла, и экран входа должен сказать это, а не
  // показывать ту же форму молча.
  function start({ afterLogin = false } = {}) {
    const my = ++gen.current
    setState((prev) => ({ ...prev, status: 'loading' }))
    const session = mode === 'web' ? fetchSession() : createSession(getInitData())
    return session
      .then((s) => fetchRouters().then((data) => ({ s, list: data?.routers ?? [] })))
      .then(({ s, list }) => {
        if (my !== gen.current) return
        // До setState: навигация из адреса и статус «готово» -- один рендер,
        // иначе синхронизация адреса успела бы стереть место пустой навигацией.
        readyRef.current?.(list, { isAdmin: Boolean(s?.is_admin) })
        // Номер человека в Telegram нужен экрану пустого доступа: его он
        // просит передать администратору, и взять его больше неоткуда.
        setState({ status: 'ready', isAdmin: Boolean(s?.is_admin), telegramUserID: Number(s?.telegram_user_id) || 0, routers: list, notice: '' })
      })
      .catch((err) => {
        if (my !== gen.current) return
        const failure = bootFailure(err, mode)
        if (afterLogin && failure.status === 'login' && err?.code === 'unauthorized') failure.notice = COOKIE_NOT_SAVED_TEXT
        setState((prev) => ({ ...prev, ...failure }))
      })
  }

  function expire() {
    gen.current++
    setState((prev) => ({ ...prev, status: 'login', notice: SESSION_EXPIRED_TEXT }))
  }

  // Выход не ждёт удачи запроса: человек нажал «Выйти» и должен оказаться на
  // экране входа, даже если сеть моргнула.
  function logout() {
    gen.current++
    return dashboardLogout()
      .catch(() => null)
      .then(() => setState({ status: 'login', isAdmin: false, telegramUserID: 0, routers: [], notice: '' }))
  }

  function setRouters(list) {
    setState((prev) => ({ ...prev, routers: list ?? [] }))
  }

  function refreshRouters() {
    const my = gen.current
    return fetchRouters()
      .then((data) => {
        if (my === gen.current) setRouters(data?.routers ?? [])
      })
      .catch(() => {})
  }

  return { ...state, start, expire, logout, setRouters, refreshRouters }
}
