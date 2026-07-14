import { useEffect, useState } from 'preact/hooks'
import { initTelegram, getInitData, getThemeParams, onThemeChanged } from './telegram.js'
import { createSession } from './api.js'
import { RouterList } from './screens/RouterList.jsx'
import { RouterDetail } from './screens/RouterDetail.jsx'

function applyTheme() {
  const theme = getThemeParams()
  const root = document.documentElement
  for (const [key, value] of Object.entries(theme)) {
    root.style.setProperty(`--tg-${key.replace(/_/g, '-')}`, value)
  }
}

function initialRouterID() {
  const params = new URLSearchParams(window.location.search)
  const raw = params.get('router')
  const id = raw ? Number(raw) : NaN
  return Number.isFinite(id) ? id : null
}

export function App() {
  const [status, setStatus] = useState('loading')
  const [selectedID, setSelectedID] = useState(initialRouterID)
  const [isAdmin, setIsAdmin] = useState(false)

  useEffect(() => {
    initTelegram()
    applyTheme()
    const offTheme = onThemeChanged(applyTheme)
    createSession(getInitData())
      .then((s) => { setIsAdmin(!!s.is_admin); setStatus('ready') })
      .catch(() => setStatus('error'))
    return offTheme
  }, [])

  if (status === 'loading') return <p class="state">Загрузка…</p>
  if (status === 'error') {
    return (
      <p class="state state-error">
        Не удалось войти. Откройте mini-app из Telegram заново.
      </p>
    )
  }

  if (selectedID != null) {
    return <RouterDetail id={selectedID} onBack={() => setSelectedID(null)} isAdmin={isAdmin} />
  }
  return <RouterList onSelect={setSelectedID} />
}
