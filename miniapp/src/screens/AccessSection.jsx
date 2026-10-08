import { useEffect, useState } from 'preact/hooks'
import { fetchAccess, fetchPeople, addOperator, removeOperator, unbindOwner, setOwner } from '../api.js'
import { livePick, peopleByID, personTitle, pickView } from '../people.js'
import { localSheet } from '../sheet.js'
import { errorText } from '../errorText.js'
import { placeText } from '../places.js'
import { SectionHeading } from '../ui/Section.jsx'

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
  const [ownerID, setOwnerID] = useState('')
  // Справочник людей (v0.58): undefined -- ещё грузится, null -- его нет
  // (старый бэкенд ответил 404 или запрос не прошёл): тогда экран прежний,
  // с вводом номера. Без справочника выдать доступ всё равно можно.
  const [people, setPeople] = useState(undefined)
  const [opPick, setOpPick] = useState(0)
  const [ownerPick, setOwnerPick] = useState(0)
  const [opQuery, setOpQuery] = useState('')
  const [ownerQuery, setOwnerQuery] = useState('')

  const loadPeople = () =>
    fetchPeople()
      .then((list) => setPeople(list))
      .catch(() => setPeople(null))

  useEffect(() => {
    fetchAccess(routerID)
      .then(setAccess)
      .catch((err) => setLoadError(errorText(err)))
    loadPeople()
  }, [routerID])

  // Любое удачное изменение доступа сбрасывает ОБА выбора: человек мог
  // получить роль в соседней форме, и прежний выбор в ней уже не в силе.
  // Справочник перечитывается: роли в подписях поменялись.
  function applyChange(data) {
    setAccess(data)
    setOpPick(0)
    setOwnerPick(0)
    if (people) loadPeople()
  }

  function runMutation(call) {
    setBusy(true)
    setActionError(null)
    call()
      .then(applyChange)
      .catch((err) => setActionError(errorText(err)))
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

  function addByID(id) {
    setBusy(true)
    setActionError(null)
    addOperator(routerID, id)
      .then((data) => {
        applyChange(data)
        setNewID('')
      })
      .catch((err) => setActionError(errorText(err)))
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
    addByID(id)
  }

  function handleAddPicked(e) {
    e.preventDefault()
    if (opLive) addByID(opLive)
  }

  // Назначение владельца -- номером человека или «меня»: тогда номер берёт
  // бэкенд из сессии. Роутер без владельца и операторов -- роутер, о поломках
  // которого не узнает никто, поэтому форма стоит прямо под этим
  // предупреждением.
  function assignOwner(owner) {
    setBusy(true)
    setActionError(null)
    setOwner(routerID, owner)
      .then((data) => {
        applyChange(data)
        setOwnerID('')
      })
      .catch((err) =>
        setActionError(
          err?.code === 'owner_exists'
            ? 'У роутера уже есть владелец — сначала отвяжите его.'
            : errorText(err),
        ),
      )
      .finally(() => setBusy(false))
  }

  function handleAssignOwner(e) {
    e.preventDefault()
    const trimmed = ownerID.trim()
    const id = Number(trimmed)
    if (!trimmed || !Number.isInteger(id) || id <= 0) {
      setActionError('Введите положительный числовой ID')
      return
    }
    assignOwner({ telegram_user_id: id })
  }

  function handleAssignPicked(e) {
    e.preventDefault()
    if (ownerLive) assignOwner({ telegram_user_id: ownerLive })
  }

  if (loadError) return <p class="state state-error">{loadError}</p>
  if (access == null) return <p class="state">Загрузка…</p>

  const operators = access.operators ?? []
  const byID = peopleByID(people)
  const picking = Array.isArray(people)
  const opView = pickView(people, { access, role: 'operator', query: opQuery })
  const ownerView = pickView(people, { access, role: 'owner', query: ownerQuery })
  // Отправляется только выбор, который виден и не отмечен.
  const opLive = livePick(opView.shown, opPick)
  const ownerLive = livePick(ownerView.shown, ownerPick)

  // Поиск сменился и выбранного не стало видно -- выбор сброшен, а не
  // спрятан: иначе он всплыл бы снова при пустом поиске.
  const searchFor = (role, setQuery, pick, setPick) => (q) => {
    setQuery(q)
    if (pick && !livePick(pickView(people, { access, role, query: q }).shown, pick)) setPick(0)
  }

  const ownerManual = (
    <form class="access-add-row" onSubmit={handleAssignOwner}>
      <div class="field">
        <label for="access-set-owner">Номер владельца в Telegram</label>
        <input
          id="access-set-owner"
          type="text"
          inputmode="numeric"
          value={ownerID}
          disabled={busy}
          onInput={(e) => setOwnerID(e.currentTarget.value)}
        />
      </div>
      <button class="btn btn-ghost" type="submit" disabled={busy}>
        Назначить
      </button>
    </form>
  )

  const operatorManual = (
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
      <button class="btn btn-ghost" type="submit" disabled={busy}>
        Добавить
      </button>
    </form>
  )

  return (
    <section class="section">
      <SectionHeading>Доступ</SectionHeading>
      <p class="admin-note">
        Кто видит этот роутер в приложении и получает уведомления о нём. Уведомления приходят
        каждому в личку; выключить их каждый может себе сам: {placeText('notifyMe')}.
      </p>

      <div class="access-group">
        <SectionHeading class="access-subtitle" deeper>Владелец</SectionHeading>
        <ul class="card list-reset access-owner">
          <li class="row">
            {access.owner ? (
              <PersonName id={access.owner.telegram_user_id} byID={byID} />
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
        {!access.owner && (
          <>
            {people === undefined ? (
              <p class="state">Загрузка списка людей…</p>
            ) : picking ? (
              <>
                <form class="access-pick access-pick-owner" onSubmit={handleAssignPicked}>
                  <PeoplePicker
                    id="access-find-owner"
                    label="Кого назначить владельцем"
                    empty={people.length === 0}
                    view={ownerView}
                    query={ownerQuery}
                    onQuery={searchFor('owner', setOwnerQuery, ownerPick, setOwnerPick)}
                    busy={busy}
                    picked={ownerLive}
                    onPick={setOwnerPick}
                  />
                  <button class="btn btn-ghost" type="submit" disabled={busy || !ownerLive}>
                    Назначить
                  </button>
                </form>
                <ManualEntry kind="owner">{ownerManual}</ManualEntry>
              </>
            ) : (
              ownerManual
            )}
            <button class="btn" type="button" disabled={busy} onClick={() => assignOwner({ me: true })}>
              Назначить меня владельцем
            </button>
          </>
        )}
      </div>

      <div class="access-group">
        <SectionHeading class="access-subtitle" deeper>Кому ещё открыт доступ</SectionHeading>
        {operators.length === 0 ? (
          <p class="muted">Кроме владельца, доступа ни у кого нет.</p>
        ) : (
          <ul class="card list-reset access-operators">
            {operators.map((op) => (
              <li key={op.telegram_user_id} class="row">
                <PersonName id={op.telegram_user_id} byID={byID} />
                <button
                  class="btn btn-danger btn-icon"
                  disabled={busy}
                  aria-label={`Удалить оператора ${nameOf(op.telegram_user_id, byID)}`}
                  onClick={() => runMutation(() => removeOperator(routerID, op.telegram_user_id))}
                >
                  ✖
                </button>
              </li>
            ))}
          </ul>
        )}

        {people === undefined ? (
          <p class="state">Загрузка списка людей…</p>
        ) : picking ? (
          <>
            <form class="access-pick access-pick-operator" onSubmit={handleAddPicked}>
              <PeoplePicker
                id="access-find-operator"
                label="Кому открыть доступ"
                empty={people.length === 0}
                view={opView}
                query={opQuery}
                onQuery={searchFor('operator', setOpQuery, opPick, setOpPick)}
                busy={busy}
                picked={opLive}
                onPick={setOpPick}
              />
              <button class="btn btn-ghost" type="submit" disabled={busy || !opLive}>
                Добавить
              </button>
            </form>
            <ManualEntry kind="operator">{operatorManual}</ManualEntry>
            <p class="admin-note">
              Человека нет в списке? Пусть откроет бота и нажмёт /start — он появится здесь по имени.
              Добавленный увидит роутер в приложении и начнёт получать уведомления о нём.
            </p>
          </>
        ) : (
          <>
            {operatorManual}
            <p class="admin-note">
              Свой номер человек видит в приложении: пока доступа нет, оно показывает его Telegram ID.
              Пусть пришлёт его вам. Добавленный увидит роутер в приложении и начнёт получать уведомления о нём.
            </p>
          </>
        )}
      </div>

      {actionError && <p class="state state-error">{actionError}</p>}
    </section>
  )
}

function nameOf(id, byID) {
  const p = byID.get(id)
  return p ? personTitle(p) : String(id)
}

// Человек в списке доступа: имя и @ник, номер подписью. Нет в справочнике --
// голый номер, как до v0.58.
function PersonName({ id, byID }) {
  const p = byID.get(id)
  const title = p ? personTitle(p) : ''
  if (!p || title.startsWith('номер ')) return <span class="access-id">{id}</span>
  return (
    <span class="person-name">
      <span class="person-title">{title}</span>
      <span class="person-sub">номер {id}</span>
    </span>
  )
}

function ManualEntry({ kind, children }) {
  return (
    <details class={`access-manual access-manual-${kind}`}>
      <summary>Ввести номер вручную</summary>
      {children}
    </details>
  )
}

// Поиск + список людей. Уже имеющие роль на этом роутере видны, но не
// выбираются: повторная выдача той же роли ничего бы не дала.
function PeoplePicker({ id, label, empty, view, query, onQuery, busy, picked, onPick }) {
  const { shown, rest } = view
  return (
    <div class="person-picker">
      <div class="field">
        <label for={id}>{label}</label>
        <input
          id={id}
          type="search"
          placeholder="Имя, @ник, номер или роутер"
          autocomplete="off"
          value={query}
          disabled={busy}
          onInput={(e) => onQuery(e.currentTarget.value)}
        />
      </div>
      {empty ? (
        <p class="muted">В списке пока никого.</p>
      ) : shown.length === 0 ? (
        <p class="muted">Никого не нашлось.</p>
      ) : (
        <ul class="card list-reset person-list">
          {shown.map((r) => (
            <li key={r.id}>
              <button
                type="button"
                class={`person-pick${picked === r.id ? ' is-picked' : ''}`}
                aria-pressed={picked === r.id ? 'true' : 'false'}
                disabled={busy || Boolean(r.taken)}
                onClick={() => onPick(picked === r.id ? 0 : r.id)}
              >
                <span class="person-title">{r.title}</span>
                <span class="person-sub">{r.sub}</span>
                {r.taken && <span class="person-taken">{r.taken}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
      {rest > 0 && <p class="field-hint">Ещё {rest} — уточните поиск.</p>}
    </div>
  )
}
