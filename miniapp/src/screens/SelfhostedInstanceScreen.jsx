import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchSelfhosted, createSelfhosted, updateSelfhosted, toggleSelfhosted, deleteSelfhosted, checkSelfhosted } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  SELFHOSTED_GROUPS,
  SELFHOSTED_TEXTS,
  instanceFormValues,
  fieldPlaceholder,
  validateInstance,
  instanceRequestBody,
  instanceChanged,
  passwordHint,
  checkResultView,
  toggleLabel,
  toggleDoneText,
  deleteInstanceSheetText,
  deleteConfirmPhrase,
  selfhostedErrorText,
  errorFieldKey,
  sshWipeWarning,
  sshHostWarning,
  SSH_WIPE_TEXT,
  SSH_CHANGED_TEXT,
} from '../selfhostedForm.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { Quoted } from '../ui/Q.jsx'
import { TextField } from '../ui/FormField.jsx'

// Экран своего сервера: форма группами (адрес для клиентов, контейнер и
// пути, SSH), «Проверить подключение», вкл/выкл, удалить. Только админ.
//
// SSH-пароль живёт только в состоянии этого экрана: не в навигации, не в
// адресе, не в консоли. Уходит в тело запроса, только когда введён, и
// стирается сразу после отправки -- до ответа сервера -- и при уходе с экрана.
// Сохранение пароль не проверяет (спека, решение 3): внешние баны и
// чувствительность панели; проверка -- отдельной кнопкой.
export function SelfhostedInstanceScreen({ instanceId = '', backLabel = 'Свои серверы', openSheet, onClose }) {
  const isNew = !instanceId
  const [inst, setInst] = useState(null)
  const [initial, setInitial] = useState(() => (isNew ? instanceFormValues(null) : null))
  const [values, setValues] = useState(() => (isNew ? instanceFormValues(null) : null))
  const [loadError, setLoadError] = useState('')
  // Значения сервера по умолчанию (defaults ответа списка) -- плейсхолдеры
  // пустых полей: пустое поле и значит «по умолчанию».
  const [defaults, setDefaults] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [check, setCheck] = useState(null)
  // Поле, которое отверг сервер (invalid_field): { key, text }.
  const [fieldError, setFieldError] = useState(null)
  const [toggling, setToggling] = useState(false)

  const alive = useRef(true)
  const valuesRef = useRef(values)
  valuesRef.current = values
  useEffect(
    () => () => {
      alive.current = false
      // Ссылка на значения могла пережить экран в замыкании -- пароль в ней
      // не остаётся.
      if (valuesRef.current) valuesRef.current.ssh_password = ''
    },
    [],
  )

  useEffect(() => {
    fetchSelfhosted()
      .then((resp) => {
        if (!alive.current) return
        setDefaults(resp?.defaults ?? null)
        // Новому серверу список нужен только ради defaults.
        if (isNew) return
        const found = (resp?.instances ?? []).find((i) => String(i.id) === instanceId)
        if (!found) {
          setLoadError(SELFHOSTED_TEXTS.notFound)
          return
        }
        const v = instanceFormValues(found)
        setInst(found)
        setInitial(v)
        setValues(v)
      })
      .catch(() => {
        // Новый сервер заполняется и без списка: плейсхолдеры останутся зашитыми.
        if (alive.current && !isNew) setLoadError(SELFHOSTED_TEXTS.loadError)
      })
  }, [instanceId])

  // После сохранения -- сервер заново: он чистит поля (пустой адрес SSH
  // стирает пользователя и порт, пустое название становится коротким именем).
  // Пароль в форме к этому моменту уже пуст.
  function reload() {
    fetchSelfhosted()
      .then((resp) => {
        if (!alive.current) return
        const found = (resp?.instances ?? []).find((i) => String(i.id) === instanceId)
        if (!found) return
        const v = instanceFormValues(found)
        setDefaults(resp?.defaults ?? null)
        setInst(found)
        setInitial(v)
        setValues(v)
      })
      .catch(() => {})
  }

  // Отвергнутое поле -- в фокус: человек сразу видит, что править.
  useEffect(() => {
    if (fieldError) document.getElementById(`sh-${fieldError.key}`)?.focus()
  }, [fieldError])

  // Отказ сервера: у знакомого поля слова под ним, иначе -- над формой.
  // Короткое имя у сохранённого сервера не показывается -- его ошибка тоже над формой.
  function showFailure(err) {
    const key = errorFieldKey(err)
    if (key && (isNew || key !== 'id')) {
      setError('')
      setFieldError({ key, text: selfhostedErrorText(err) })
      return
    }
    setFieldError(null)
    setError(selfhostedErrorText(err))
  }

  function set(key, value) {
    setFieldError((prev) => (prev?.key === key ? null : prev))
    setValues((prev) => ({ ...prev, [key]: value }))
    setError('')
    setNotice('')
  }

  function save(e) {
    e.preventDefault()
    if (!values || busy) return
    const problem = validateInstance(values, { isNew, passwordSet: inst?.password_set === true, saved: inst })
    if (problem === SSH_CHANGED_TEXT) {
      // Ошибка про пароль -- у поля пароля: туда и вводить.
      setError('')
      setNotice('')
      setFieldError({ key: 'ssh_password', text: problem })
      return
    }
    if (problem) {
      setError(problem)
      setNotice('')
      return
    }
    if (!isNew && !instanceChanged(initial, values, { isNew })) {
      setError('')
      setNotice(SELFHOSTED_TEXTS.nothing)
      return
    }
    const body = instanceRequestBody(values, { isNew })
    // Адрес SSH стёрт у сервера с паролем: сервер сотрёт и пароль. Введённый
    // пароль без адреса не хранится -- в тело он не идёт вовсе.
    const wipe = !isNew && sshWipeWarning(inst, values) !== ''
    if (wipe) delete body.ssh_password
    const passwordSent = 'ssh_password' in body
    // Тело сериализуется при вызове; дальше пароль не нужен ни в теле, ни
    // в поле.
    const send = () => (isNew ? createSelfhosted(body) : updateSelfhosted(instanceId, body))
    const pending = wipe ? null : send()
    delete body.ssh_password
    const cleared = { ...values, ssh_password: '' }
    values.ssh_password = ''
    valuesRef.current = cleared
    setValues(cleared)
    setError('')
    setNotice('')
    setFieldError(null)

    const saved = () => {
      setInitial(cleared)
      setInst((prev) => ({
        ...prev,
        ssh_host: body.ssh_host,
        password_set: body.ssh_host === '' ? false : passwordSent || prev?.password_set === true,
      }))
      setNotice(SELFHOSTED_TEXTS.saved)
      reload()
    }

    if (wipe) {
      openSheet(
        localSheet({
          title: 'Сохранить без адреса SSH?',
          body: `${SSH_WIPE_TEXT}. Пользователь и порт SSH тоже сотрутся, а выпуск пойдёт через контейнер на той же машине, что и сервер wg-monitor.`,
          buttonLabel: 'Сохранить',
          busyLabel: 'Сохраняем…',
          danger: true,
          errorText: (err) => {
            if (alive.current) showFailure(err)
            return selfhostedErrorText(err)
          },
          perform: send,
          onDone: () => {
            if (alive.current) saved()
          },
        }),
      )
      return
    }

    setBusy(true)
    pending
      .then(() => {
        if (!alive.current) return
        if (isNew) {
          onClose()
          return
        }
        saved()
      })
      .catch((err) => {
        if (alive.current) showFailure(err)
      })
      .finally(() => {
        if (alive.current) setBusy(false)
      })
  }

  function runCheck() {
    if (check?.busy) return
    setCheck({ busy: true })
    checkSelfhosted(instanceId)
      .then((resp) => {
        if (alive.current) setCheck(checkResultView(resp))
      })
      .catch((err) => {
        if (alive.current) setCheck({ tone: 'bad', text: selfhostedErrorText(err) })
      })
  }

  function toggle() {
    if (!inst || toggling) return
    const next = !inst.enabled
    setToggling(true)
    setError('')
    setNotice('')
    toggleSelfhosted(instanceId, next)
      .then(() => {
        if (!alive.current) return
        setInst((prev) => ({ ...prev, enabled: next }))
        setNotice(toggleDoneText(next))
      })
      .catch((err) => {
        if (alive.current) setError(selfhostedErrorText(err))
      })
      .finally(() => {
        if (alive.current) setToggling(false)
      })
  }

  function remove() {
    const text = deleteInstanceSheetText(inst)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Удалить',
        busyLabel: 'Удаляем…',
        danger: true,
        confirmPhrase: deleteConfirmPhrase(inst),
        confirmStrict: true,
        errorText: selfhostedErrorText,
        perform: (typed) => deleteSelfhosted(instanceId, typed),
        onDone: () => onClose(),
      }),
    )
  }

  const title = isNew ? SELFHOSTED_TEXTS.newTitle : inst ? `Сервер «${inst.label || inst.id}»` : 'Сервер'

  return (
    <Overlay title={title} backLabel={backLabel} onBack={onClose}>
      <form class="screen connection selfhosted-form" onSubmit={save} autocomplete="off" noValidate>
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        {loadError ? (
          <p class="state state-error">{loadError}</p>
        ) : !values ? (
          <p class="state">{SELFHOSTED_TEXTS.loading}</p>
        ) : (
          <>
            {!isNew && inst && (
              <Section title="Состояние">
                <div class="card selfhosted-state">
                  <p class="traffic-detail">
                    {inst.enabled ? 'Включён: с него выпускаются VPN-туннели.' : 'Выключен: выпускать с него VPN-туннели нельзя.'}
                  </p>
                  <div class="selfhosted-actions">
                    <button type="button" class="btn btn-ghost" disabled={toggling} onClick={toggle}>
                      {toggling ? 'Сохраняем…' : toggleLabel(inst)}
                    </button>
                    <button type="button" class="btn btn-ghost" disabled={check?.busy === true} onClick={runCheck}>
                      {check?.busy ? SELFHOSTED_TEXTS.checking : SELFHOSTED_TEXTS.checkButton}
                    </button>
                  </div>
                  <p class="field-hint">{SELFHOSTED_TEXTS.checkHint}</p>
                  {check && !check.busy && (
                    <p class={`selfhosted-check selfhosted-check-${check.tone}`} role="status">
                      <Quoted text={check.text} />
                    </p>
                  )}
                </div>
              </Section>
            )}
            {SELFHOSTED_GROUPS.map((group) => (
              <Section key={group.title} title={group.title}>
                <div class="card form-group">
                  {group.fields
                    .filter((f) => isNew || !f.newOnly)
                    .map((f) => (
                      <TextField
                        key={f.key}
                        id={`sh-${f.key}`}
                        label={f.label}
                        type={f.kind === 'password' ? 'password' : 'text'}
                        value={values[f.key]}
                        placeholder={fieldPlaceholder(f, defaults)}
                        inputMode={f.inputMode}
                        hint={f.kind === 'password' ? passwordHint(inst, { isNew }) : f.hint}
                        error={fieldError?.key === f.key ? fieldError.text : ''}
                        warn={f.key === 'ssh_host' && !isNew ? sshHostWarning(inst, values) : ''}
                        onInput={(v) => set(f.key, v)}
                      />
                    ))}
                </div>
                {group.note && <p class="field-hint">{group.note}</p>}
              </Section>
            ))}
            <p class="field-hint connection-keep">{SELFHOSTED_TEXTS.keepNote}</p>
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
                {busy ? 'Сохраняем…' : isNew ? SELFHOSTED_TEXTS.add : 'Сохранить'}
              </button>
              {!isNew && inst && (
                <button type="button" class="btn btn-ghost cabinet-danger" onClick={remove}>
                  Удалить сервер
                </button>
              )}
            </div>
          </>
        )}
      </form>
    </Overlay>
  )
}
