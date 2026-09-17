import { useEffect, useRef } from 'preact/hooks'
import { FLEET_FILTERS, isFilterShortcut } from '../fleetFilter.js'

// Поле поиска и чипы фильтров над списком роутеров. Чипы переносятся на
// следующую строку, а не прокручиваются вбок: горизонтальной прокрутки в
// приложении нет ни на одной ширине.
//
// shortcut -- слушать ли «/». Оболочка гасит его, пока открыт лист: курсор
// не должен уезжать из подтверждения в список под затемнением.
export function FleetFilterBar({ query, filter, counts, onQuery, onFilter, shortcut = true }) {
  const inputRef = useRef(null)
  const shortcutRef = useRef(shortcut)
  shortcutRef.current = shortcut

  useEffect(() => {
    const onKey = (e) => {
      if (!shortcutRef.current || !isFilterShortcut(e)) return
      e.preventDefault()
      inputRef.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div class="filter-bar" role="search">
      <input
        ref={inputRef}
        type="search"
        class="filter-search"
        placeholder="Поиск: имя, версия, тип"
        aria-label="Поиск роутеров"
        autocomplete="off"
        autocapitalize="off"
        spellcheck={false}
        value={query}
        onInput={(e) => onQuery(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && query) onQuery('')
        }}
      />
      <div class="filter-chips" role="group" aria-label="Показать роутеры">
        {FLEET_FILTERS.map((f) => (
          <button
            key={f.key}
            type="button"
            class={`filter-chip${filter === f.key ? ' filter-chip-active' : ''}`}
            aria-pressed={filter === f.key ? 'true' : 'false'}
            onClick={() => onFilter(f.key)}
          >
            <span class="filter-chip-label">{f.label}</span>
            <span class="filter-chip-count">{counts?.[f.key] ?? 0}</span>
          </button>
        ))}
      </div>
    </div>
  )
}
