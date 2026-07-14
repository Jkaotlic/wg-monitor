import { useEffect, useState } from 'preact/hooks'
import { fetchAccess, addOperator, removeOperator, unbindOwner } from '../api.js'

// Admin-only "Доступ" block on RouterDetail. Backend enforces admin
// independently (see miniappRequireAdmin) -- this component is only ever
// mounted when the caller already knows isAdmin, so it's purely UX gating,
// not a security boundary.
export function AccessSection({ routerID }) {
  const [access, setAccess] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState(null)
  const [newID, setNewID] = useState('')

  useEffect(() => {
    fetchAccess(routerID)
      .then(setAccess)
      .catch((err) => setLoadError(err.message))
  }, [routerID])

  function runMutation(call) {
    setBusy(true)
    setActionError(null)
    call()
      .then(setAccess)
      .catch((err) => setActionError(err.message))
      .finally(() => setBusy(false))
  }

  function handleAdd(e) {
    e.preventDefault()
    const trimmed = newID.trim()
    const id = Number(trimmed)
    if (!trimmed || !Number.isInteger(id) || id <= 0) {
      setActionError('Введите положительный числовой ID')
      return
    }
    setBusy(true)
    setActionError(null)
    addOperator(routerID, id)
      .then((data) => {
        setAccess(data)
        setNewID('')
      })
      .catch((err) => setActionError(err.message))
      .finally(() => setBusy(false))
  }

  if (loadError) return <p class="state state-error">{loadError}</p>
  if (access == null) return <p class="state">Загрузка…</p>

  const operators = access.operators ?? []

  return (
    <section class="section">
      <h2 class="section-title">Доступ</h2>

      <div class="access-group">
        <h3 class="access-subtitle">Владелец</h3>
        <ul class="card list-reset">
          <li class="row">
            {access.owner ? (
              <span class="access-id">{access.owner.telegram_user_id}</span>
            ) : (
              <span class="muted">не привязан</span>
            )}
            {access.owner && (
              <button
                class="btn btn-danger"
                disabled={busy}
                onClick={() => runMutation(() => unbindOwner(routerID))}
              >
                Отвязать
              </button>
            )}
          </li>
        </ul>
      </div>

      <div class="access-group">
        <h3 class="access-subtitle">Операторы</h3>
        {operators.length === 0 ? (
          <p class="muted">нет операторов</p>
        ) : (
          <ul class="card list-reset">
            {operators.map((op) => (
              <li key={op.telegram_user_id} class="row">
                <span class="access-id">{op.telegram_user_id}</span>
                <button
                  class="btn btn-danger btn-icon"
                  disabled={busy}
                  aria-label={`Удалить оператора ${op.telegram_user_id}`}
                  onClick={() => runMutation(() => removeOperator(routerID, op.telegram_user_id))}
                >
                  ✖
                </button>
              </li>
            ))}
          </ul>
        )}

        <form class="access-add-row" onSubmit={handleAdd}>
          <div class="field">
            <label for="access-add-operator">ID оператора</label>
            <input
              id="access-add-operator"
              type="text"
              inputmode="numeric"
              value={newID}
              disabled={busy}
              onInput={(e) => setNewID(e.currentTarget.value)}
            />
          </div>
          <button class="btn btn-primary" type="submit" disabled={busy}>
            Добавить
          </button>
        </form>
      </div>

      {actionError && <p class="state state-error">{actionError}</p>}
    </section>
  )
}
