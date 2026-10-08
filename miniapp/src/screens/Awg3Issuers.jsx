import { useEffect, useState } from 'preact/hooks'
import { addAwg3Issuer, removeAwg3Issuer, fetchPeople } from '../api.js'
import { issuersTaken, livePick, peopleByID, pickView } from '../people.js'
import { Section } from '../ui/Section.jsx'
import { ManualEntry, PeoplePicker, PersonName, nameOf } from './PeoplePicker.jsx'

// Кто может выпускать конфиги с этой панели на свои роутеры (v0.51).
// Экран панели и так закрыт для не-админов; граница доступа -- сервер.
//
// v0.58.1: человек выбирается из справочника, как на экране доступа к
// роутеру; ручной ввод номера -- свёрнутый запасной путь. Справочника нет
// (старый бэкенд, ошибка первой загрузки) -- прежнее поле номера.
export function Awg3Issuers({ panel, onChanged }) {
  const [newID, setNewID] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // undefined -- ещё грузится, null -- справочника нет, массив -- есть.
  const [people, setPeople] = useState(undefined)
  const [query, setQuery] = useState('')
  const [pick, setPick] = useState(0)
  const list = panel?.issuers ?? []

  // Неудачная перезагрузка после правки оставляет прежний список: только
  // первая загрузка решает, есть ли справочник вообще.
  const loadPeople = (first = false) =>
    fetchPeople()
      .then((ppl) => (first || Array.isArray(ppl) ? setPeople(ppl) : null))
      .catch(() => (first ? setPeople(null) : null))

  useEffect(() => {
    loadPeople(true)
  }, [])

  const taken = issuersTaken(list)
  const view = pickView(people, { taken, query })
  const live = livePick(view.shown, pick)
  const byID = peopleByID(people)

  function run(p) {
    setBusy(true)
    setError('')
    return p
      .then((resp) => {
        setNewID('')
        setPick(0)
        onChanged?.(resp?.panel)
        if (people) loadPeople()
      })
      .catch((err) => setError(String(err?.serverMessage ?? '').trim() || 'Не получилось сохранить — попробуйте ещё раз.'))
      .finally(() => setBusy(false))
  }

  function addManual(e) {
    e.preventDefault()
    const t = newID.trim()
    if (!/^[1-9][0-9]{0,14}$/.test(t)) {
      setError('Введите положительный числовой ID')
      return
    }
    run(addAwg3Issuer(panel.id, Number(t)))
  }

  function addPicked(e) {
    e.preventDefault()
    if (live) run(addAwg3Issuer(panel.id, live))
  }

  // Скрытый поиском выбор не отправляется и не возвращается сам.
  function onQuery(q) {
    setQuery(q)
    if (pick && !livePick(pickView(people, { taken, query: q }).shown, pick)) setPick(0)
  }

  const manual = (
    <form class="access-add-row" onSubmit={addManual}>
      <div class="field">
        <label for="awg3-issuer-id">Номер человека в Telegram</label>
        <input id="awg3-issuer-id" type="text" inputmode="numeric" value={newID} disabled={busy} onInput={(e) => setNewID(e.currentTarget.value)} />
      </div>
      <button class="btn btn-ghost" type="submit" disabled={busy}>
        Добавить
      </button>
    </form>
  )

  return (
    <Section title="Кто может выпускать конфиги">
      <p class="admin-note">Выпускает конфиги с этой панели на свои роутеры. Пароль и адрес панели не видит.</p>
      {list.length > 0 && (
        <ul class="card list-reset awg3-issuers-list">
          {list.map((is) => (
            <li key={is.telegram_user_id} class="row">
              <PersonName id={is.telegram_user_id} byID={byID} />
              <button
                type="button"
                class="btn btn-danger btn-icon"
                aria-label={`Убрать ${nameOf(is.telegram_user_id, byID)}`}
                disabled={busy}
                onClick={() => run(removeAwg3Issuer(panel.id, is.telegram_user_id))}
              >
                ✖
              </button>
            </li>
          ))}
        </ul>
      )}
      {Array.isArray(people) ? (
        <>
          <form onSubmit={addPicked}>
            <PeoplePicker
              id="awg3-issuer-pick"
              label="Кому разрешить"
              empty={people.length === 0}
              view={view}
              query={query}
              onQuery={onQuery}
              busy={busy}
              picked={live}
              onPick={setPick}
            />
            <button class="btn btn-ghost" type="submit" disabled={busy || !live}>
              Добавить
            </button>
          </form>
          <ManualEntry kind="issuer">{manual}</ManualEntry>
          <p class="field-hint">Человека нет в списке? Пусть откроет бота и нажмёт /start — он появится здесь по имени.</p>
        </>
      ) : (
        people === null && manual
      )}
      {error && <p class="state state-error">{error}</p>}
    </Section>
  )
}
