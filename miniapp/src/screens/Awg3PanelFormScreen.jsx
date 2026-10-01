import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchAwg3Panels, createAwg3Panel, updateAwg3Panel, deleteAwg3Panel } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  AWG3_TEXTS,
  PANEL_FIELDS,
  panelFormValues,
  validatePanelForm,
  panelRequestBody,
  awg3ErrorText,
  awg3FieldKey,
  checkView,
  certHint,
  passwordHint,
  deletePanelSheetText,
  readFileBase64,
  p12SizeProblem,
} from '../awg3Panel.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { Quoted } from '../ui/Q.jsx'
import { TextField } from '../ui/FormField.jsx'

// Добавить или изменить awg3-панель. Только админ. Пароль панели, файл .p12 и
// его пароль живут только в состоянии этого экрана: не в навигации, не в
// адресе, не в localStorage; стираются сразу после отправки -- до ответа --
// и при уходе с экрана. «Сохранить и проверить» -- ровно один запрос к
// панели (его делает сервер).
export function Awg3PanelFormScreen({ panelId = '', backLabel = 'Свои серверы', openSheet, onClose, onDeleted }) {
  // После создания экран становится правкой той же панели.
  const [savedId, setSavedId] = useState(panelId)
  const isNew = !savedId
  const [panel, setPanel] = useState(null)
  const [values, setValues] = useState(() => (panelId ? null : panelFormValues(null)))
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [fieldError, setFieldError] = useState(null)
  const [check, setCheck] = useState(null)

  const alive = useRef(true)
  const valuesRef = useRef(values)
  valuesRef.current = values
  // Правка 3 (ревью раунд 1): нативный input type=file не шлёт change на тот
  // же файл повторно, пока его .value не сброшен -- ref нужен, чтобы сбросить
  // его и после чтения, и после отправки (save её обнуляет отдельно).
  const fileInputRef = useRef(null)
  useEffect(
    () => () => {
      alive.current = false
      const v = valuesRef.current
      if (v) Object.assign(v, { password: '', p12_password: '', p12_base64: '' })
    },
    [],
  )

  useEffect(() => {
    if (!panelId) return
    fetchAwg3Panels()
      .then((resp) => {
        if (!alive.current) return
        const found = (resp?.panels ?? []).find((p) => String(p.id) === panelId)
        if (!found) {
          setLoadError(AWG3_TEXTS.notFound)
          return
        }
        setPanel(found)
        setValues(panelFormValues(found))
      })
      .catch(() => {
        if (alive.current) setLoadError(AWG3_TEXTS.loadError)
      })
  }, [panelId])

  useEffect(() => {
    if (fieldError) document.getElementById(`a3-${fieldError.key}`)?.focus()
  }, [fieldError])

  function set(key, value) {
    setFieldError((prev) => (prev?.key === key ? null : prev))
    setValues((prev) => ({ ...prev, [key]: value }))
    setError('')
    setCheck(null)
  }

  async function pickFile(e) {
    const input = e.currentTarget
    const file = input.files?.[0]
    if (!file) return
    // Правка 5: потолок 100 КБ -- до чтения, файл явно не сертификат.
    const big = p12SizeProblem(file.size)
    if (big) {
      setFieldError({ key: 'p12', text: big })
      setError('')
      input.value = ''
      return
    }
    try {
      const b64 = await readFileBase64(file)
      if (!alive.current) return
      setValues((prev) => ({ ...prev, p12_base64: b64, p12_name: file.name }))
      setFieldError((prev) => (prev?.key === 'p12' ? null : prev))
      setError('')
    } catch {
      if (alive.current) setFieldError({ key: 'p12', text: AWG3_TEXTS.p12ReadError })
    } finally {
      // Сброс .value -- иначе повторный выбор ТОГО ЖЕ файла не пришлёт change.
      input.value = ''
    }
  }

  function showFailure(err) {
    const key = awg3FieldKey(err)
    if (key && (isNew || key !== 'id')) {
      setError('')
      setFieldError({ key, text: awg3ErrorText(err) })
      return
    }
    setFieldError(null)
    setError(awg3ErrorText(err))
  }

  function save(e) {
    e.preventDefault()
    if (!values || busy) return
    const problem = validatePanelForm(values, { isNew, saved: panel })
    if (problem) {
      setError('')
      setFieldError({ key: problem.field, text: problem.text })
      return
    }
    const body = panelRequestBody(values, { isNew })
    const pending = isNew ? createAwg3Panel(body) : updateAwg3Panel(savedId, body)
    // Тело уже сериализовано: секреты не нужны ни в нём, ни в полях.
    delete body.password
    delete body.p12_base64
    delete body.p12_password
    const cleared = { ...values, password: '', p12_password: '', p12_base64: '', p12_name: '' }
    Object.assign(values, { password: '', p12_password: '', p12_base64: '' })
    valuesRef.current = cleared
    setValues(cleared)
    // Тот же сброс, что в pickFile: иначе повторный выбор одного и того же
    // файла после отказа сервера не пришлёт change.
    if (fileInputRef.current) fileInputRef.current.value = ''
    setError('')
    setFieldError(null)
    setCheck(null)
    setBusy(true)
    pending
      .then((resp) => {
        if (!alive.current) return
        if (resp?.panel) {
          setPanel(resp.panel)
          setSavedId(String(resp.panel.id))
          setValues(panelFormValues(resp.panel))
        }
        setCheck(checkView(resp?.check ?? null))
      })
      .catch((err) => {
        if (alive.current) showFailure(err)
      })
      .finally(() => {
        if (alive.current) setBusy(false)
      })
  }

  function remove() {
    const text = deletePanelSheetText(panel)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Удалить',
        busyLabel: 'Удаляем…',
        danger: true,
        confirmPhrase: text.phrase,
        confirmStrict: true,
        errorText: awg3ErrorText,
        perform: (typed) => deleteAwg3Panel(savedId, typed),
        onDone: () => (onDeleted ?? onClose)(),
      }),
    )
  }

  const title = isNew ? AWG3_TEXTS.newTitle : panel ? `Панель VPN-сервера «${panel.label || panel.id}»` : 'Панель VPN-сервера'
  const fe = (key) => (fieldError?.key === key ? fieldError.text : '')

  return (
    <Overlay title={title} backLabel={backLabel} onBack={onClose}>
      <form class="screen connection awg3-form" onSubmit={save} autocomplete="off" noValidate>
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        {loadError ? (
          <p class="state state-error">{loadError}</p>
        ) : !values ? (
          <p class="state">{AWG3_TEXTS.loading}</p>
        ) : (
          <>
            <Section title="Панель">
              <div class="card form-group">
                {PANEL_FIELDS.filter((f) => isNew || !f.newOnly).map((f) => (
                  <TextField
                    key={f.key}
                    id={`a3-${f.key}`}
                    label={f.label}
                    type={f.kind === 'password' ? 'password' : 'text'}
                    value={values[f.key]}
                    placeholder={f.placeholder ?? ''}
                    inputMode={f.inputMode}
                    hint={f.kind === 'password' ? passwordHint(panel, { isNew }) : f.hint}
                    error={fe(f.key)}
                    onInput={(v) => set(f.key, v)}
                  />
                ))}
              </div>
            </Section>
            <Section title={AWG3_TEXTS.certSection}>
              <div class="card form-group">
                <div class={fe('p12') ? 'field field-error awg3-file' : 'field awg3-file'}>
                  <label for="a3-p12">{AWG3_TEXTS.p12Label}</label>
                  {/* Правка 6: родная подпись «Choose File / No file chosen» --
                      по-английски и её не перекрасить; вход спрятан, кнопка --
                      своя, имя файла показывает certHint из состояния формы.
                      Ревью раунд 2: tabindex=-1 -- невидимый вход не должен
                      быть отдельной остановкой Tab перед кнопкой; label
                      сверху по-прежнему даёт ему доступное имя. */}
                  <input ref={fileInputRef} id="a3-p12" type="file" tabindex="-1" accept=".p12,.pfx,application/x-pkcs12" onChange={pickFile} />
                  <button type="button" class="btn btn-ghost btn-wide awg3-file-btn" onClick={() => fileInputRef.current?.click()}>
                    {AWG3_TEXTS.p12Pick}
                  </button>
                  {fe('p12') ? (
                    <p class="field-error-text" role="alert">
                      {fe('p12')}
                    </p>
                  ) : (
                    <p class="field-hint">{certHint(panel, values)}</p>
                  )}
                </div>
                <TextField
                  id="a3-p12_password"
                  label={AWG3_TEXTS.p12Password}
                  type="password"
                  value={values.p12_password}
                  hint={AWG3_TEXTS.p12PasswordHint}
                  error={fe('p12_password')}
                  onInput={(v) => set('p12_password', v)}
                />
              </div>
            </Section>
            <p class="field-hint">{AWG3_TEXTS.saveHint}</p>
            {error && (
              <p class="wizard-error" role="alert">
                {error}
              </p>
            )}
            {check && (
              <p class={`awg3-check awg3-check-${check.tone}`} role="status">
                <Quoted text={check.text} />
              </p>
            )}
            <div class="wizard-actions">
              <button type="submit" class="btn btn-primary" disabled={busy}>
                {busy ? AWG3_TEXTS.saving : isNew ? AWG3_TEXTS.saveNew : AWG3_TEXTS.saveEdit}
              </button>
              {!isNew && panel && (
                <button type="button" class="btn btn-ghost cabinet-danger" onClick={remove}>
                  {AWG3_TEXTS.deleteButton}
                </button>
              )}
            </div>
          </>
        )}
      </form>
    </Overlay>
  )
}
