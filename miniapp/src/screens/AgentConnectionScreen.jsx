import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchAgentConnection, saveAgentConnection } from '../api.js'
import {
  CONNECTION_GROUPS,
  CONNECTION_TEXTS,
  connectionChanged,
  connectionErrorText,
  connectionFormValues,
  connectionRequestBody,
  connectionSelectOptions,
  validateConnection,
} from '../agentConnection.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { Quoted } from '../ui/Q.jsx'
import { TextField, SelectField } from '../ui/FormField.jsx'

// «Подключение агента» -- как сервер добирается до роутера. Только админ:
// сервер отвечает остальным 404, вход в «Обслуживании» виден только админу.
// Сохранение без подтверждения набором: правка метаданных, роутер она не
// трогает, пока не понадобится раскатка.
export function AgentConnectionScreen({ routerID, routerName, onClose }) {
  const [initial, setInitial] = useState(null)
  const [values, setValues] = useState(null)
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const alive = useRef(true)
  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  useEffect(() => {
    setInitial(null)
    setValues(null)
    setLoadError('')
    fetchAgentConnection(routerID)
      .then((resp) => {
        if (!alive.current) return
        const v = connectionFormValues(resp)
        setInitial(v)
        setValues(v)
      })
      .catch(() => {
        if (alive.current) setLoadError(CONNECTION_TEXTS.loadError)
      })
  }, [routerID])

  function set(key, value) {
    setValues((prev) => ({ ...prev, [key]: value }))
    setError('')
    setNotice('')
  }

  function save(e) {
    e.preventDefault()
    if (!values || busy) return
    const problem = validateConnection(values)
    if (problem) {
      setError(problem)
      setNotice('')
      return
    }
    if (!connectionChanged(initial, values)) {
      setError('')
      setNotice(CONNECTION_TEXTS.nothing)
      return
    }
    const snapshot = values
    setBusy(true)
    setError('')
    setNotice('')
    saveAgentConnection(routerID, connectionRequestBody(snapshot))
      .then(() => {
        if (!alive.current) return
        setInitial(snapshot)
        setNotice(CONNECTION_TEXTS.saved)
      })
      .catch((err) => {
        if (alive.current) setError(connectionErrorText(err))
      })
      .finally(() => {
        if (alive.current) setBusy(false)
      })
  }

  const title = routerName ? `Подключение агента «${routerName}»` : 'Подключение агента'

  return (
    <Overlay title={title} backLabel="Обслуживание" onBack={onClose}>
      <form class="screen connection" onSubmit={save} autocomplete="off" noValidate>
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        <p class="hint">{CONNECTION_TEXTS.intro}</p>
        {loadError ? (
          <p class="state state-error">{loadError}</p>
        ) : !values ? (
          <p class="state">Загрузка…</p>
        ) : (
          <>
            {CONNECTION_GROUPS.map((group) => (
              <Section key={group.title} title={group.title}>
                <div class="card form-group">
                  {group.fields.map((f) =>
                    f.kind === 'select' ? (
                      <SelectField
                        key={f.key}
                        id={`conn-${f.key}`}
                        label={f.label}
                        value={values[f.key]}
                        options={connectionSelectOptions(f, values[f.key])}
                        onChange={(v) => set(f.key, v)}
                      />
                    ) : (
                      <TextField
                        key={f.key}
                        id={`conn-${f.key}`}
                        label={f.label}
                        value={values[f.key]}
                        placeholder={f.placeholder}
                        inputMode={f.inputMode}
                        onInput={(v) => set(f.key, v)}
                      />
                    ),
                  )}
                </div>
              </Section>
            ))}
            <p class="field-hint connection-keep">{CONNECTION_TEXTS.keepNote}</p>
            {error && (
              <p class="wizard-error" role="alert">
                {error}
              </p>
            )}
            {notice && (
              <p class="hint connection-notice" role="status">
                {notice}
              </p>
            )}
            <div class="wizard-actions">
              <button type="submit" class="btn btn-primary" disabled={busy}>
                {busy ? 'Сохраняем…' : 'Сохранить'}
              </button>
            </div>
          </>
        )}
      </form>
    </Overlay>
  )
}
