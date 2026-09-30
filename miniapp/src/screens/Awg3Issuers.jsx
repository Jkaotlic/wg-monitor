import { useState } from 'preact/hooks'
import { addAwg3Issuer, removeAwg3Issuer } from '../api.js'
import { Section } from '../ui/Section.jsx'

// Кто может выпускать конфиги с этой панели на свои роутеры (v0.51).
// Экран панели и так закрыт для не-админов; граница доступа -- сервер.
export function Awg3Issuers({ panel, onChanged }) {
  const [newID, setNewID] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const list = panel?.issuers ?? []

  function run(p) {
    setBusy(true)
    setError('')
    return p
      .then((resp) => {
        setNewID('')
        onChanged?.(resp?.panel)
      })
      .catch(() => setError('Не получилось сохранить — попробуйте ещё раз.'))
      .finally(() => setBusy(false))
  }

  function add(e) {
    e.preventDefault()
    const t = newID.trim()
    if (!/^[1-9][0-9]{0,15}$/.test(t)) {
      setError('Введите положительный числовой ID')
      return
    }
    run(addAwg3Issuer(panel.id, Number(t)))
  }

  return (
    <Section title="Кто может выпускать конфиги">
      <p class="admin-note">Выпускает конфиги с этой панели на свои роутеры. Пароль и адрес панели не видит.</p>
      {list.length > 0 && (
        <ul class="card list-reset">
          {list.map((is) => (
            <li key={is.telegram_user_id} class="row">
              <span class="access-id">{is.telegram_user_id}</span>
              <button
                type="button"
                class="btn btn-danger btn-icon"
                aria-label={`Убрать ${is.telegram_user_id}`}
                disabled={busy}
                onClick={() => run(removeAwg3Issuer(panel.id, is.telegram_user_id))}
              >
                ✖
              </button>
            </li>
          ))}
        </ul>
      )}
      <form class="access-add-row" onSubmit={add}>
        <div class="field">
          <label for="awg3-issuer-id">Номер человека в Telegram</label>
          <input id="awg3-issuer-id" type="text" inputmode="numeric" value={newID} disabled={busy} onInput={(e) => setNewID(e.currentTarget.value)} />
        </div>
        <button class="btn btn-ghost" type="submit" disabled={busy}>
          Добавить
        </button>
      </form>
      {error && <p class="state state-error">{error}</p>}
    </Section>
  )
}
