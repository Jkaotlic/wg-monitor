import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchSelfhosted, fetchAwg3Panels } from '../api.js'
import { instanceRows, SELFHOSTED_TEXTS } from '../selfhostedForm.js'
import { AWG3_TEXTS, panelRows } from '../awg3Panel.js'
import { Overlay } from '../ui/Overlay.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { Section } from '../ui/Section.jsx'

// «Свои VPN-серверы» -- Парк, только админ. Серверы общие для всех роутеров:
// слой всего парка, в адрес пишется (?open=selfhosted). Ниже -- awg3-панели
// отдельной группой (v0.49); группа есть, только когда экран умеет их
// открывать (onOpenAwg3), и только тогда панели спрашиваются.
export function SelfhostedScreen({ backLabel = 'Назад', onClose, onOpenInstance, onOpenAwg3, onAddAwg3 }) {
  const [rows, setRows] = useState(null)
  const [error, setError] = useState('')
  const [panels, setPanels] = useState(null)
  const [panelsError, setPanelsError] = useState('')
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
    if (onOpenAwg3) {
      fetchAwg3Panels()
        .then((resp) => {
          if (alive.current) setPanels(panelRows(resp?.panels))
        })
        .catch((err) => {
          if (alive.current) setPanelsError(err?.code === 'awg3_not_configured' ? AWG3_TEXTS.notConfigured : AWG3_TEXTS.loadError)
        })
    }
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
        {onOpenAwg3 && (
          <Section title={AWG3_TEXTS.group}>
            <p class="hint">{AWG3_TEXTS.groupIntro}</p>
            {panelsError ? (
              <p class="state state-error">{panelsError}</p>
            ) : panels == null ? (
              <p class="state">{AWG3_TEXTS.loading}</p>
            ) : panels.length === 0 ? (
              <p class="state">{AWG3_TEXTS.empty}</p>
            ) : (
              <ul class="card list-reset settings-card awg3-list">
                {panels.map((p) => (
                  <ListRow key={p.id} title={p.title} sub={p.sub} tone={p.tone} onClick={() => onOpenAwg3(p.id)} />
                ))}
              </ul>
            )}
            {!panelsError && (
              <button type="button" class="btn btn-ghost btn-wide awg3-add" onClick={() => onAddAwg3?.()}>
                {AWG3_TEXTS.add}
              </button>
            )}
          </Section>
        )}
      </div>
    </Overlay>
  )
}
