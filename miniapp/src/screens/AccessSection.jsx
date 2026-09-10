import { useEffect, useState } from 'preact/hooks'
import { fetchAccess, addOperator, removeOperator, unbindOwner } from '../api.js'
import { localSheet } from '../sheet.js'

// Admin-only "Доступ" block on RouterDetail. Backend enforces admin
// independently (see miniappRequireAdmin) -- this component is only ever
// mounted when the caller already knows isAdmin, so it's purely UX gating,
// not a security boundary.
export function AccessSection({ routerID, openSheet }) {
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

  // Отвязка владельца -- единственное здесь действие, которое может оставить
  // роутер без единого адресата: уведомления идут в личку, и слать их станет
  // некому. Молча этого делать нельзя.
  const askUnbindOwner = () => {
    const apply = () => runMutation(() => unbindOwner(routerID))
    const alone = (access?.operators ?? []).length === 0
    if (!openSheet) {
      apply()
      return
    }
    openSheet(
      localSheet({
        title: 'Отвязать владельца?',
        body: alone
          ? 'Он перестанет видеть роутер в приложении. Больше доступа нет ни у кого — значит уведомления о поломках этого роутера не придут никому.'
          : 'Он перестанет видеть роутер в приложении и получать уведомления о нём. Доступ останется у операторов ниже.',
        buttonLabel: 'Отвязать',
        danger: true,
        perform: apply,
      }),
    )
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
      <p class="admin-note">
        Кто видит этот роутер в приложении и получает уведомления о нём. Уведомления приходят
        каждому в личку; выключить их каждый может себе сам на экране «Настройки».
      </p>

      <div class="access-group">
        <h3 class="access-subtitle">Владелец</h3>
        <ul class="card list-reset">
          <li class="row">
            {access.owner ? (
              <span class="access-id">{access.owner.telegram_user_id}</span>
            ) : (
              // Роутер без владельца и операторов -- это роутер, о поломках
              // которого не узнает никто: уведомления идут в личку, а слать
              // их некому. Раньше это состояние было невидимым.
              <span class="muted">не привязан — уведомления о роутере никому не приходят</span>
            )}
            {access.owner && (
              <button
                class="btn btn-danger"
                disabled={busy}
                onClick={askUnbindOwner}
              >
                Отвязать
              </button>
            )}
          </li>
        </ul>
      </div>

      <div class="access-group">
        <h3 class="access-subtitle">Кому ещё открыт доступ</h3>
        {operators.length === 0 ? (
          <p class="muted">Кроме владельца, доступа ни у кого нет.</p>
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
            <label for="access-add-operator">Номер человека в Telegram</label>
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
        <p class="admin-note">
          Свой номер человек узнаёт у бота командой <b>/myid</b> — пусть пришлёт его вам.
          Добавленный увидит роутер в приложении и начнёт получать уведомления о нём.
        </p>
      </div>

      {actionError && <p class="state state-error">{actionError}</p>}
    </section>
  )
}
