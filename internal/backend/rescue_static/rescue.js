'use strict'
// Аварийная страница: вход, сводка, раскатка бэкенда. Без сборки и без
// внешних ресурсов. Всё, что пришло от сервера, выводится только через
// textContent -- никакой разметки из ответов.
const POLL_MS = 3000
const WAIT_MS = 5 * 60 * 1000

const el = (id) => document.getElementById(id)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
let deploying = false

function setText(id, text) {
  el(id).textContent = text
}

function setNote(id, text, tone) {
  const node = el(id)
  node.textContent = text
  node.className = tone ? `note ${tone}` : 'note'
}

function setState(text, bad) {
  const node = el('state')
  node.textContent = text
  node.className = bad ? 'state bad' : 'state'
}

async function readJSON(res) {
  try {
    return await res.json()
  } catch (e) {
    return null
  }
}

function when(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString('ru-RU')
}

function num(v) {
  return typeof v === 'number' && Number.isFinite(v) ? String(v) : '—'
}

function showLogin(message) {
  el('app').hidden = true
  el('login').hidden = false
  setText('login-error', message || '')
}

function showApp() {
  el('login').hidden = true
  el('app').hidden = false
}

function watchdogLine(wd) {
  const parts = [wd.alive ? 'работает' : `не работает${wd.reason ? ` (${wd.reason})` : ''}`]
  if (wd.last_scan_at) parts.push(`последний обход ${when(wd.last_scan_at)}`)
  // Неразрывный пробел: число не отрывается от подписи при переносе.
  parts.push(`молчат\u00a0${num(wd.stale_users)}`)
  parts.push(`заглушено\u00a0${num(wd.suppressed_users)}`)
  return `Сторож: ${parts.join(' · ')}`
}

function render(s) {
  setText('b-version', s.version || '—')
  setText('b-latest', s.latest_version || '—')
  setText('b-generated', when(s.generated_at))
  const t = s.totals || {}
  setText('f-agents', num(t.agents))
  setText('f-online', num(t.online))
  setText('f-sleeping', num(t.sleeping))
  setText('f-offline', num(t.offline))
  setText('f-alerts', num(t.alerts))
  const wd = el('f-watchdog')
  if (s.watchdog) {
    wd.textContent = watchdogLine(s.watchdog)
    wd.className = s.watchdog.alive ? 'note' : 'note bad'
    wd.hidden = false
  } else {
    wd.hidden = true
  }
  if (!el('d-version').value) el('d-version').value = s.latest_version || s.version || ''
  syncDeploy()
}

async function load() {
  setState('Загрузка сводки…')
  let res
  try {
    res = await fetch('/v1/dashboard/summary', { credentials: 'same-origin', cache: 'no-store' })
  } catch (e) {
    setState('Сервер не ответил. Обновите страницу чуть позже.', true)
    return
  }
  if (res.status === 401) {
    setState('')
    showLogin('')
    return
  }
  if (!res.ok) {
    // Сводка сломана, но раскатка от неё не зависит -- аварийная страница
    // нужна именно тогда, когда что-то сломано.
    const body = await readJSON(res)
    setState(`Сводка не получена: код ${res.status}${body && body.code ? ` ${body.code}` : ''}. Раскатка доступна.`, true)
    showApp()
    syncDeploy()
    return
  }
  const summary = await readJSON(res)
  setState('')
  showApp()
  render(summary || {})
}

el('login-form').addEventListener('submit', async (e) => {
  e.preventDefault()
  const token = el('token').value.trim()
  if (!token) {
    setText('login-error', 'Введите токен')
    return
  }
  el('login-btn').disabled = true
  setText('login-error', '')
  try {
    const res = await fetch('/v1/dashboard/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    })
    if (res.ok) {
      el('token').value = ''
      await load()
      return
    }
    if (res.status === 401) {
      setText('login-error', 'Токен не подошёл')
      return
    }
    const body = await readJSON(res)
    if (res.status === 429) {
      setText('login-error', (body && body.message) || 'Слишком много попыток. Подождите и попробуйте снова.')
      return
    }
    setText('login-error', `Вход не удался: код ${res.status}`)
  } catch (err) {
    setText('login-error', 'Сервер не ответил')
  } finally {
    el('login-btn').disabled = false
  }
})

el('logout').addEventListener('click', async () => {
  try {
    await fetch('/v1/dashboard/logout', { method: 'POST', credentials: 'same-origin' })
  } catch (e) {
    // Кука HttpOnly: без ответа сервера её не стереть, но форма всё равно
    // покажется -- следующий запрос скажет правду.
  }
  el('d-version').value = ''
  el('d-confirm').value = ''
  el('d-downgrade').checked = false
  setNote('d-status', '')
  showLogin('')
})

function syncDeploy() {
  const target = el('d-version').value.trim()
  el('d-go').disabled = deploying || !target || el('d-confirm').value.trim() !== target
}

el('d-version').addEventListener('input', syncDeploy)
el('d-confirm').addEventListener('input', syncDeploy)

function deployErrorText(status, body) {
  const code = body && body.code ? body.code : ''
  if (code === 'downgrade_rejected') return 'Это откат — отметьте флажок «Это откат — разрешить»'
  const message = body && body.message ? body.message : ''
  return `Ошибка ${status}${code ? ` ${code}` : ''}${message ? `: ${message}` : ''}`
}

el('deploy-form').addEventListener('submit', async (e) => {
  e.preventDefault()
  const target = el('d-version').value.trim()
  if (!target || el('d-confirm').value.trim() !== target) {
    setNote('d-status', 'Наберите версию ещё раз в поле подтверждения', 'warn')
    return
  }
  deploying = true
  syncDeploy()
  setNote('d-status', 'Отправляем заявку…')
  let res
  try {
    res = await fetch('/v1/dashboard/backend/deploy', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target_version: target, allow_downgrade: el('d-downgrade').checked }),
    })
  } catch (err) {
    deploying = false
    syncDeploy()
    setNote('d-status', 'Сервер не ответил — заявка не отправлена', 'bad')
    return
  }
  if (res.status === 202) {
    await waitFor(target)
    return
  }
  deploying = false
  syncDeploy()
  if (res.status === 401) {
    showLogin('Сессия закончилась — войдите снова')
    return
  }
  setNote('d-status', deployErrorText(res.status, await readJSON(res)), 'bad')
})

async function waitFor(target) {
  const started = Date.now()
  setNote('d-status', `Заявка принята. Бэкенд перезапускается на ${target}…`, 'warn')
  for (;;) {
    await sleep(POLL_MS)
    if (Date.now() - started > WAIT_MS) {
      deploying = false
      syncDeploy()
      setNote('d-status', `Бэкенд не ответил версией ${target} за 5 минут. Проверьте журнал обновления на сервере и обновите страницу.`, 'bad')
      return
    }
    try {
      const res = await fetch('/healthz', { cache: 'no-store' })
      const health = res.ok ? await readJSON(res) : null
      if (health && health.version === target) {
        setNote('d-status', `Готово, бэкенд ${target}`, 'ok')
        await sleep(1500)
        window.location.reload()
        return
      }
    } catch (err) {
      // Пока бэкенд перезапускается, запрос падает -- это и есть ожидание.
    }
    setNote('d-status', `Бэкенд перезапускается… прошло ${Math.round((Date.now() - started) / 1000)} с`, 'warn')
  }
}

load()
