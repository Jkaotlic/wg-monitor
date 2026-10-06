import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchSelfhosted, createSelfhosted, updateSelfhosted, toggleSelfhosted, deleteSelfhosted, checkSelfhosted, confirmSelfhostedHostKey, fetchSelfhostedClients, revokeSelfhostedClient } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  SELFHOSTED_GROUPS,
  SELFHOSTED_TEXTS,
  instanceFormValues,
  fieldPlaceholder,
  groupSummary,
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
  HOSTKEY_TEXTS,
  hostKeyView,
  hostKeyConfirmLabel,
  confirmHostKeySheetText,
  CLIENTS_TEXTS,
  clientRows,
  revokeSheetText,
} from '../selfhostedForm.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { Fold } from '../ui/Fold.jsx'
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
export function SelfhostedInstanceScreen({ instanceId = '', backLabel = 'Свои VPS', openSheet, onClose }) {
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
  // Выданные подключения: null -- ещё не читали; { busy } | { rows } | { error }.
  const [clients, setClients] = useState(null)

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
      setFieldError({ key: 'ssh_password', text: 'Введите пароль SSH заново.' })
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
      .finally(refreshHostKey)
  }

  // Вход мог запомнить первый ключ или отказанный ожидающим (C1): подтянуть
  // только отпечатки -- набранное в форме не трогается, в отличие от reload.
  function refreshHostKey() {
    fetchSelfhosted()
      .then((resp) => {
        if (!alive.current) return
        const found = (resp?.instances ?? []).find((i) => String(i.id) === instanceId)
        if (!found) return
        setInst((prev) =>
          prev
            ? { ...prev, ssh_host_key: found.ssh_host_key, ssh_host_key_pending: found.ssh_host_key_pending, ssh_host_key_pending_at: found.ssh_host_key_pending_at }
            : prev,
        )
      })
      .catch(() => {})
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

  // «Подтвердить ключ сервера «SHA256:…»» (C1): набором названия, как удаление.
  // Уходит именно тот отпечаток, что админ видит в карточке; бэкенд сверяет
  // его с ожидающим. После -- перечитать сервер.
  function confirmHostKey() {
    const fingerprint = hostKeyView(inst)?.pending || ''
    if (!fingerprint) return
    const text = confirmHostKeySheetText(inst)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Подтвердить',
        busyLabel: 'Сохраняем…',
        danger: true,
        confirmPhrase: deleteConfirmPhrase(inst),
        confirmStrict: true,
        errorText: selfhostedErrorText,
        // Отказ (сервер успел предъявить другой ключ или ожидающего уже нет) --
        // карточка сама подтягивает отпечатки, слова отказа -- в листе.
        perform: (typed) =>
          confirmSelfhostedHostKey(instanceId, typed, fingerprint).catch((err) => {
            refreshHostKey()
            throw err
          }),
        onDone: () => {
          if (!alive.current) return
          setInst((prev) => ({ ...prev, ssh_host_key: fingerprint, ssh_host_key_pending: '', ssh_host_key_pending_at: undefined }))
          setCheck(null)
          setError('')
          setNotice(HOSTKEY_TEXTS.confirmed)
          reload()
        },
      }),
    )
  }

  // Читает сервер по SSH -- только по кнопке, не при открытии экрана.
  function loadClients() {
    if (clients?.busy) return
    setClients({ busy: true })
    fetchSelfhostedClients(instanceId)
      .then((resp) => {
        if (alive.current) setClients({ rows: clientRows(resp) })
      })
      .catch((err) => {
        if (alive.current) setClients({ error: selfhostedErrorText(err) })
      })
  }

  function revokeClient(row) {
    const text = revokeSheetText(inst, row)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: CLIENTS_TEXTS.revoke,
        busyLabel: CLIENTS_TEXTS.revoking,
        danger: true,
        confirmPhrase: deleteConfirmPhrase(inst),
        confirmStrict: true,
        errorText: selfhostedErrorText,
        perform: (typed) => revokeSelfhostedClient(instanceId, row.id, typed),
        onDone: () => {
          if (!alive.current) return
          setError('')
          setNotice(CLIENTS_TEXTS.revoked)
          loadClients()
        },
      }),
    )
  }

  const hostKey = hostKeyView(inst)
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
                  <div class="selfhosted-actions action-row action-row-pair">
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
                  {hostKey && (
                    <div class="selfhosted-hostkey">
                      <p class="selfhosted-hostkey-label">{HOSTKEY_TEXTS.label}</p>
                      {hostKey.pending ? (
                        <>
                          <p class="selfhosted-hostkey-changed selfhosted-check-bad" role="status">{HOSTKEY_TEXTS.changed}</p>
                          <div class="selfhosted-hostkey-row">
                            <p class="selfhosted-hostkey-label">{HOSTKEY_TEXTS.was}</p>
                            <code class="selfhosted-hostkey-value">{hostKey.fingerprint}</code>
                          </div>
                          <div class="selfhosted-hostkey-row">
                            <p class="selfhosted-hostkey-label">{HOSTKEY_TEXTS.now}</p>
                            <code class="selfhosted-hostkey-value">{hostKey.pending}</code>
                          </div>
                          {hostKey.seenText && <p class="field-hint">{hostKey.seenText}</p>}
                          <button type="button" class="btn btn-ghost cabinet-danger selfhosted-hostkey-confirm" onClick={confirmHostKey}>
                            {hostKeyConfirmLabel(hostKey.pending)}
                          </button>
                        </>
                      ) : hostKey.fingerprint ? (
                        <code class="selfhosted-hostkey-value">{hostKey.fingerprint}</code>
                      ) : (
                        <p class="field-hint">{HOSTKEY_TEXTS.unknown}</p>
                      )}
                    </div>
                  )}
                </div>
              </Section>
            )}
            {!isNew && inst && inst.enabled && (
              <Section title={CLIENTS_TEXTS.title}>
                <div class="card selfhosted-clients">
                  <p class="field-hint">{CLIENTS_TEXTS.hint}</p>
                  {clients?.busy && <p class="state">{CLIENTS_TEXTS.loading}</p>}
                  {clients?.error && (
                    <p class="state state-error" role="alert">
                      <Quoted text={clients.error} />
                    </p>
                  )}
                  {clients?.rows && clients.rows.length === 0 && <p class="traffic-detail">{CLIENTS_TEXTS.empty}</p>}
                  {clients?.rows?.map((row) => (
                    <div key={row.id} class="selfhosted-client">
                      <p class="selfhosted-client-name">{row.name}</p>
                      <p class="field-hint">{[row.address, row.date].filter(Boolean).join(' · ')}</p>
                      {row.inUse && (
                        <p class="selfhosted-client-warn">
                          <Quoted text={row.inUse} />
                        </p>
                      )}
                      <button type="button" class="btn btn-ghost cabinet-danger" onClick={() => revokeClient(row)}>
                        {CLIENTS_TEXTS.revoke}
                      </button>
                    </div>
                  ))}
                  <button type="button" class="btn btn-ghost" disabled={clients?.busy === true} onClick={loadClients}>
                    {clients && !clients.busy ? CLIENTS_TEXTS.refresh : CLIENTS_TEXTS.show}
                  </button>
                </div>
              </Section>
            )}
            {SELFHOSTED_GROUPS.map((group) => {
              const body = (
                <>
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
                          hint={f.kind === 'password' ? passwordHint(inst, { isNew }, values) : f.hint}
                          error={fieldError?.key === f.key ? fieldError.text : ''}
                          warn={f.key === 'ssh_host' && !isNew ? sshHostWarning(inst, values) : ''}
                          onInput={(v) => set(f.key, v)}
                        />
                      ))}
                  </div>
                  {group.note && <p class="field-hint">{group.note}</p>}
                </>
              )
              if (!group.fold) {
                return (
                  <Section key={group.key} title={group.title}>
                    {body}
                  </Section>
                )
              }
              // Редко правят -- свёрнуто (спека п. 3.2); ошибка поля внутри
              // раскрывает группу, иначе человек не увидел бы, куда вводить.
              const forced = group.fields.some((f) => fieldError?.key === f.key)
              return (
                <section key={group.key} class="section">
                  <Fold class="form-fold" title={group.title} note={groupSummary(group, values)} titleTag="h2" titleClass="section-title" open={forced ? true : undefined}>
                    {body}
                  </Fold>
                </section>
              )
            })}
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
