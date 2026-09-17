import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchSelfhosted } from '../api.js'
import { instanceRows, SELFHOSTED_TEXTS } from '../selfhostedForm.js'
import { Overlay } from '../ui/Overlay.jsx'
import { ListRow } from '../ui/ListRow.jsx'

// «Свои VPN-серверы» -- Парк, только админ. Серверы общие для всех роутеров:
// слой всего парка, в адрес пишется (?open=selfhosted).
export function SelfhostedScreen({ backLabel = 'Назад', onClose, onOpenInstance }) {
  const [rows, setRows] = useState(null)
  const [error, setError] = useState('')
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    fetchSelfhosted()
      .then((resp) => {
        if (alive.current) setRows(instanceRows(resp?.instances))
      })
      .catch(() => {
        if (alive.current) setError(SELFHOSTED_TEXTS.loadError)
      })
    return () => {
      alive.current = false
    }
  }, [])

  return (
    <Overlay title={SELFHOSTED_TEXTS.title} backLabel={backLabel} onBack={onClose}>
      <div class="screen selfhosted">
        <h1 class="screen-title">{SELFHOSTED_TEXTS.title}</h1>
        <p class="hint">{SELFHOSTED_TEXTS.intro}</p>
        {error ? (
          <p class="state state-error">{error}</p>
        ) : rows == null ? (
          <p class="state">{SELFHOSTED_TEXTS.loading}</p>
        ) : rows.length === 0 ? (
          <p class="state">{SELFHOSTED_TEXTS.empty}</p>
        ) : (
          <ul class="card list-reset settings-card selfhosted-list">
            {rows.map((r) => (
              <ListRow key={r.id} title={r.title} sub={r.sub} onClick={() => onOpenInstance(r.id)} />
            ))}
          </ul>
        )}
        <button type="button" class="btn btn-primary btn-wide selfhosted-add" onClick={() => onOpenInstance('')}>
          {SELFHOSTED_TEXTS.add}
        </button>
      </div>
    </Overlay>
  )
}
