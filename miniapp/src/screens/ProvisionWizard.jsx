import { useEffect, useRef, useState } from 'preact/hooks'
import { startProvision } from '../api.js'
import { confirmReady } from '../sheet.js'
import { NICKNAME_RULE } from '../formRules.js'
import {
  AGENT_KINDS,
  AWGM_AUTH_OPTIONS,
  PROVISION_PATHS,
  PROVISION_SECRET_NOTE,
  STEP_TITLES,
  TOKEN_TEXTS,
  WIZARD_SECRET_KEYS,
  clearSecrets,
  initialWizardValues,
  nextStep,
  prevStep,
  provisionErrorStep,
  provisionErrorText,
  provisionRequestBody,
  provisionSummary,
  stepError,
  stepPosition,
  tokenResultView,
  wizardSteps,
} from '../provisionWizard.js'
import { Overlay } from '../ui/Overlay.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Q, Quoted } from '../ui/Q.jsx'
import { TextField, SelectField, ChoiceList } from '../ui/FormField.jsx'
import { CopyButton } from '../ui/CopyButton.jsx'

const NO_JOB_ID_TEXT = 'Сервер не вернул номер задания — проверьте Парк: установка могла начаться.'

// Мастер «Добавить роутер» -- лист-экран, а не модалка: полей много.
// Введённое живёт только здесь, в состоянии экрана: навигация (nav) знает лишь,
// что мастер открыт. Пароли стираются при отправке -- до ответа сервера -- и
// при уходе с экрана; наружу (onStarted) уходят номер задания и ник.
//
// Пока запрос в пути, мастер закреплён (onBusy -> nav 'pin'): уйти с него
// нельзя ни одним путём, иначе запущенное задание осталось бы без «Хода
// работы», а выпущенный токен -- непоказанным. Если экран всё же размонтирован
// (смена раскладки), номер задания доходит до onStarted всё равно.
export function ProvisionWizard({ backLabel = 'Назад', onClose, onStarted, onRegistered, onBusy }) {
  const [values, setValues] = useState(initialWizardValues)
  const [step, setStep] = useState('path')
  const [error, setError] = useState('')
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [token, setToken] = useState(null)

  const alive = useRef(true)
  const valuesRef = useRef(values)
  valuesRef.current = values
  useEffect(
    () => () => {
      alive.current = false
      const live = valuesRef.current
      for (const key of WIZARD_SECRET_KEYS) live[key] = ''
    },
    [],
  )

  const set = (key) => (value) => {
    setValues((prev) => ({ ...prev, [key]: value }))
    setError('')
  }

  const nickname = values.nickname.trim()
  const pos = stepPosition(values.path, step)
  const isToken = values.path === 'token'
  const ready = nickname !== '' && confirmReady({ confirmPhrase: nickname }, typed)

  function goNext() {
    const problem = stepError(step, values)
    if (problem) {
      setError(problem)
      return
    }
    const next = nextStep(values.path, step)
    if (next) {
      setStep(next)
      setError('')
    }
  }

  function goBack() {
    if (busy) return
    const prev = prevStep(values.path, step)
    if (!prev) {
      onClose()
      return
    }
    setStep(prev)
    setError('')
  }

  function submit() {
    if (busy) return
    for (const s of wizardSteps(values.path)) {
      const problem = stepError(s, values)
      if (problem) {
        setStep(s)
        setError(problem)
        return
      }
    }
    if (!ready) return
    const body = provisionRequestBody(values, typed)
    const path = values.path
    setBusy(true)
    onBusy?.(true)
    setError('')
    const fresh = clearSecrets(values)
    valuesRef.current = fresh
    setValues(fresh)
    startProvision(body)
      .then((resp) => {
        if (body.kind === 'register') {
          if (alive.current) setToken(tokenResultView(resp))
          onBusy?.(false)
          onRegistered?.()
          return
        }
        if (!resp?.job_id) {
          onBusy?.(false)
          if (!alive.current) return
          setStep('confirm')
          setError(NO_JOB_ID_TEXT)
          setTyped('')
          return
        }
        // Номер задания уходит и с размонтированного экрана: задание уже
        // запущено, и «Ход работы» -- единственное место, где виден итог.
        onStarted({ jobId: resp.job_id, nickname: resp?.nickname || body.nickname })
      })
      .catch((err) => {
        onBusy?.(false)
        if (!alive.current) return
        setStep(provisionErrorStep(err, path))
        setError(provisionErrorText(err))
        setTyped('')
      })
      .finally(() => {
        if (alive.current) setBusy(false)
      })
  }

  function onSubmit(e) {
    e.preventDefault()
    if (step === 'confirm') submit()
    else goNext()
  }

  if (token) return <TokenResult token={token} onClose={onClose} />

  return (
    <Overlay title="Добавить роутер" backLabel={backLabel} onBack={busy ? undefined : onClose}>
      <form class="screen wizard" onSubmit={onSubmit} autocomplete="off" noValidate>
        <h1 class="screen-title">Добавить роутер</h1>
        <p class="wizard-progress">
          Шаг {pos.index} из {pos.total} · {STEP_TITLES[step]}
        </p>

        {step === 'path' && (
          <ChoiceList label="Как добавить роутер" value={values.path} options={PROVISION_PATHS} onChange={set('path')} />
        )}

        {step === 'router' && (
          <div class="wizard-fields">
            <TextField id="wizard-nickname" label="Имя роутера" value={values.nickname} onInput={set('nickname')} placeholder="dacha-1" hint={NICKNAME_RULE} />
            <ChoiceList label="Где стоит роутер" value={values.agentKind} options={AGENT_KINDS} onChange={set('agentKind')} />
          </div>
        )}

        {step === 'access' && (
          <div class="wizard-fields">
            <TextField
              id="wizard-awgm-url"
              label="Адрес панели awg-manager"
              value={values.awgmURL}
              onInput={set('awgmURL')}
              placeholder="https://router.example.com"
              hint="Внешний адрес панели (KeenDNS): сервер заходит на роутер через неё."
            />
            <TextField id="wizard-root-password" label="Пароль root" type="password" value={values.rootPassword} onInput={set('rootPassword')} />
            <SelectField id="wizard-awgm-auth" label="Вход в панель" value={values.awgmAuth} options={AWGM_AUTH_OPTIONS} onChange={set('awgmAuth')} />
            {values.awgmAuth === 'web' && (
              <>
                <TextField id="wizard-awgm-login" label="Логин панели" value={values.awgmLogin} onInput={set('awgmLogin')} />
                <TextField id="wizard-awgm-password" label="Пароль панели" type="password" value={values.awgmPassword} onInput={set('awgmPassword')} />
              </>
            )}
            {values.awgmAuth === 'api-key' && (
              <TextField id="wizard-awgm-key" label="Ключ API панели" type="password" value={values.awgmAPIKey} onInput={set('awgmAPIKey')} />
            )}
            <TextField
              id="wizard-version"
              label="Версия агента"
              value={values.version}
              onInput={set('version')}
              placeholder="последняя"
              hint="Пусто — последняя версия. Своя пишется так: v0.36.0."
            />
            <p class="wizard-note">{PROVISION_SECRET_NOTE}</p>
          </div>
        )}

        {step === 'confirm' && (
          <>
            <div class="card wizard-summary">
              {provisionSummary(values).map((row) => (
                <DataRow key={row.label} title={row.label} value={row.value} />
              ))}
            </div>
            <div class="field wizard-confirm">
              <label for="wizard-confirm-input">
                Наберите <Q>{nickname}</Q>, чтобы подтвердить
              </label>
              <input
                id="wizard-confirm-input"
                type="text"
                autocomplete="off"
                autocapitalize="off"
                spellcheck={false}
                value={typed}
                onInput={(e) => setTyped(e.currentTarget.value)}
              />
            </div>
          </>
        )}

        {error && (
          <p class="wizard-error" role="alert">
            {error}
          </p>
        )}

        <div class="wizard-actions">
          <button type="button" class="btn btn-ghost" disabled={busy} onClick={goBack}>
            {step === 'path' ? 'Отмена' : 'Назад'}
          </button>
          {step === 'confirm' ? (
            <button type="submit" class="btn btn-primary" disabled={!ready || busy}>
              {busy ? (isToken ? 'Выдаём…' : 'Запускаем…') : isToken ? 'Выдать токен' : 'Установить'}
            </button>
          ) : (
            <button type="submit" class="btn btn-primary">
              Дальше
            </button>
          )}
        </div>
      </form>
    </Overlay>
  )
}

// Экран токена: показать один раз и дать скопировать. Токен живёт только в
// состоянии мастера -- закрыли экран, и его больше нет нигде в приложении.
function TokenResult({ token, onClose }) {
  const title = `Токен для «${token.nickname}»`
  return (
    <Overlay title={title} backLabel="Готово" onBack={onClose}>
      <div class="screen wizard">
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        <p class="wizard-warn" role="alert">
          {TOKEN_TEXTS.once}
        </p>
        <div class="token-block">
          <code class="token-value">{token.token}</code>
          <CopyButton text={token.token} />
        </div>
        <div class="card">
          <DataRow title="Адрес бэкенда" value={token.backendURL} />
        </div>
        {token.installCommand && (
          <section class="section">
            <h2 class="section-title">Команда установки</h2>
            <p class="hint">{TOKEN_TEXTS.commandHint}</p>
            <div class="token-block">
              <code class="token-command">{token.installCommand}</code>
              <CopyButton text={token.installCommand} label="Скопировать команду" />
            </div>
          </section>
        )}
        <p class="hint">
          <Quoted text={TOKEN_TEXTS.after} />
        </p>
        <div class="wizard-actions">
          <button type="button" class="btn btn-primary" onClick={onClose}>
            Готово
          </button>
        </div>
      </div>
    </Overlay>
  )
}
