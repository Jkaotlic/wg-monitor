import { useEffect, useState } from 'preact/hooks'
import { initTelegram, getInitData, getThemeParams } from './telegram.js'
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

  useEffect(() => {
    initTelegram()
    applyTheme()
    createSession(getInitData())
      .then(() => setStatus('ready'))
      .catch(() => setStatus('error'))
  }, [])

  if (status === 'loading') return <p class="state-message">Загрузка…</p>
  if (status === 'error') {
    return (
      <p class="state-message state-message-error">
        Не удалось войти. Откройте mini-app из Telegram заново.
      </p>
    )
  }

  if (selectedID != null) {
    return <RouterDetail id={selectedID} onBack={() => setSelectedID(null)} />
  }
  return <RouterList onSelect={setSelectedID} />
}
