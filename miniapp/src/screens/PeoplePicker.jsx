import { personTitle } from '../people.js'

// Выбор человека из справочника (v0.58): экран доступа к роутеру и
// «Кто может выпускать конфиги» на панели VPS.

export function nameOf(id, byID) {
  const p = byID.get(id)
  return p ? personTitle(p) : String(id)
}

// Человек в списке доступа: имя и @ник, номер подписью. Нет в справочнике --
// голый номер, как до v0.58.
export function PersonName({ id, byID }) {
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

export function ManualEntry({ kind, children }) {
  return (
    <details class={`access-manual access-manual-${kind}`}>
      <summary>Ввести номер вручную</summary>
      {children}
    </details>
  )
}

// Поиск + список людей. Уже имеющие эту роль видны, но не
// выбираются: повторная выдача той же роли ничего бы не дала.
export function PeoplePicker({ id, label, empty, view, query, onQuery, busy, picked, onPick }) {
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
