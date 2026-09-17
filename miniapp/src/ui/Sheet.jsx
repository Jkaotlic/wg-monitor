import { useEffect, useRef, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { sheetPhase, confirmReady, initialFieldValues, fieldsReady } from '../sheet.js'
import { commandOutcomeLabel } from '../labels.js'
import { maintenanceOutcomeLabel, commandErrorText, commandDeadlineMs } from '../maintenance.js'
import { Q, Quoted } from './Q.jsx'

// Нижний шит -- единственное место, где приложение спрашивает "точно?" и
// показывает ход выполнения. Команду он и запускает сам: раньше это жило
// внутри кнопки, и каждая кнопка изобретала своё подтверждение заново.
//
// asleep приходит от экрана: роутер, который сейчас спит, отвечает не сразу,
// и обещать быстрый ответ было бы враньём -- поэтому и текст другой, и
// дедлайн ожидания шире.
export function Sheet({ sheet, asleep, onClose }) {
  const { busy, result, error, errorCode, sleepNote, run } = useCommand(sheet.routerID)
  // Локальное действие (sheet.perform) выполняет сам бэкенд, а не роутер:
  // ходу выполнения там неоткуда взяться, поэтому фаза остаётся «спросить»,
  // а кнопка на время запроса гаснет.
  const local = typeof sheet.perform === 'function'
  const [localBusy, setLocalBusy] = useState(false)
  const [localError, setLocalError] = useState(null)
  const phase = local ? 'confirm' : sheetPhase({ busy, result, error })
  // Набранное подтверждение живёт здесь, а не в описании шита: описание --
  // это то, что задумал экран, а набранное -- то, что делает человек прямо
  // сейчас, и смешивать их значило бы переписывать намерение вводом.
  const [typed, setTyped] = useState('')
  // Поля формы (оживление агента: пароль, логин, срок). Значения -- здесь, а
  // не в описании листа: описание лежит в состоянии App, и пароль не должен
  // туда попасть. Стираются при отправке (до ответа) и при закрытии.
  const fields = local && Array.isArray(sheet.fields) ? sheet.fields : []
  const [values, setValues] = useState(() => initialFieldValues(fields))
  const ready = confirmReady(sheet, typed) && fieldsReady(sheet, values)
  // Уход листа со страницы (закрытие, смена другим листом, уход экрана) --
  // введённое стирается и здесь: ссылка на объект значений могла пережить
  // компонент в замыкании.
  const valuesRef = useRef(values)
  valuesRef.current = values
  useEffect(
    () => () => {
      const live = valuesRef.current
      for (const k of Object.keys(live ?? {})) live[k] = ''
    },
    [],
  )

  function setField(name, value) {
    setValues((prev) => ({ ...prev, [name]: value }))
  }

  // Смонтирован ли лист. Ответ локального действия может прийти, когда лист
  // уже ушёл, а на его месте открыт другой: закрыть тогда -- значит закрыть
  // чужой лист.
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  // Лист занят: команда ушла на роутер или локальное действие ждёт ответа
  // сервера (пароль root оживления уже отправлен). Закрыть его в это время --
  // оставить человека без ответа на то, что он уже сделал.
  const locked = phase === 'running' || localBusy

  function close() {
    if (fields.length) setValues(initialFieldValues(fields))
    onClose()
  }

  function dismiss() {
    if (!locked) close()
  }

  // Esc -- то же, что клик по затемнению и «Отмена»: пока лист не занят,
  // закрывает, во время выполнения молчит.
  const escRef = useRef(null)
  escRef.current = dismiss
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'Escape') escRef.current?.()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  function start() {
    if (local) {
      setLocalBusy(true)
      setLocalError(null)
      const typedNow = sheet.confirmPhrase ? typed : ''
      const submitted = fields.length ? values : {}
      if (fields.length) {
        // Ссылка -- сразу на чистые значения: стирание при уходе листа не
        // должно задеть снимок, который ещё не дошёл до perform.
        const fresh = initialFieldValues(fields)
        valuesRef.current = fresh
        setValues(fresh)
      }
      Promise.resolve()
        .then(() => sheet.perform(typedNow, submitted))
        .then((resp) => {
          if (sheet.onDone) sheet.onDone(resp)
          if (alive.current) close()
        })
        .catch((err) => {
          if (!alive.current) return
          const text = typeof sheet.errorText === 'function' ? sheet.errorText(err) : ''
          setLocalError(text || 'Не получилось. Попробуйте ещё раз.')
        })
        .finally(() => {
          if (alive.current) setLocalBusy(false)
        })
      return
    }
    // Набранное имя уходит серверу: для прошивки и перезагрузки он сверяет
    // его сам, и проверка на экране -- только пауза для человека.
    const confirm = sheet.confirmPhrase ? typed : ''
    run(sheet.action, sheet.args, { deadlineMs: commandDeadlineMs(sheet.action, asleep), confirm }).then((res) => {
      // onResult -- любой исход, для экранов, которым нужен сам ответ роутера
      // (сброс DNS: путь снимка, частичный успех). onDone -- только успех.
      if (res && sheet.onResult) sheet.onResult(res)
      if (res?.status === 'ok' && sheet.onDone) sheet.onDone()
    })
  }

  return (
    <div class="sheet-layer">
      {/* Подложка закрывает шит только до запуска: обрывать наблюдение за
          уже ушедшей на роутер командой случайным тапом мимо -- плохая идея. */}
      <div class="sheet-scrim" onClick={locked ? undefined : dismiss} />
      <div class="sheet">
        <div class="sheet-grip" />
        {/* Заголовок и текст шита собирают экраны готовыми строками с
            именами внутри; имена ложатся в <Q> здесь, в одном месте. */}
        <p class="sheet-title">
          <Quoted text={sheet.title} />
        </p>
        <p class="sheet-body">
          <Quoted text={sheet.body} />
        </p>

        {phase === 'confirm' && (
          <>
            {asleep && !local && (
              <p class="sheet-note">Роутер сейчас не на связи. Команда подождёт его несколько минут и отменится, если он не проснётся.</p>
            )}
            {/* Локальное действие роутеру не уходит -- показывать имя команды
                нечего, и строка «команда: undefined» была бы враньём. */}
            {!local && (
              <div class="sheet-command">
                <span class="sheet-command-label">команда</span>
                <span class="sheet-command-value">{sheet.commandLabel || sheet.action}</span>
              </div>
            )}
            {fields.length > 0 && (
              <div class="sheet-fields">
                {fields.map((f) => (
                  <div class="field" key={f.name}>
                    <label for={`sheet-field-${f.name}`}>{f.label}</label>
                    {f.type === 'select' ? (
                      <select
                        id={`sheet-field-${f.name}`}
                        value={values[f.name] ?? ''}
                        onChange={(e) => setField(f.name, e.currentTarget.value)}
                      >
                        {(f.options ?? []).map((o) => (
                          <option key={o.value} value={o.value}>{o.label}</option>
                        ))}
                      </select>
                    ) : (
                      <input
                        id={`sheet-field-${f.name}`}
                        type={f.type === 'password' ? 'password' : 'text'}
                        autocomplete="off"
                        autocapitalize="off"
                        spellcheck={false}
                        placeholder={f.placeholder ?? ''}
                        value={values[f.name] ?? ''}
                        onInput={(e) => setField(f.name, e.currentTarget.value)}
                      />
                    )}
                  </div>
                ))}
              </div>
            )}
            {local && sheet.note && <p class="sheet-note">{sheet.note}</p>}
            {localError && <p class="state state-error">{localError}</p>}
            {sheet.confirmPhrase && (
              <div class="field sheet-confirm">
                <label for="sheet-confirm-input">
                  Наберите <Q>{sheet.confirmPhrase}</Q>, чтобы подтвердить
                </label>
                <input
                  id="sheet-confirm-input"
                  type="text"
                  autocomplete="off"
                  value={typed}
                  onInput={(e) => setTyped(e.currentTarget.value)}
                />
              </div>
            )}
            <div class="sheet-actions">
              <button type="button" class="btn btn-ghost" disabled={localBusy} onClick={dismiss}>Отмена</button>
              <button
                type="button"
                class={`btn ${sheet.danger ? 'btn-danger' : 'btn-primary'}`}
                disabled={!ready || localBusy}
                onClick={start}
              >
                {localBusy ? (sheet.busyLabel || 'Сохраняем…') : sheet.buttonLabel}
              </button>
            </div>
          </>
        )}

        {phase === 'running' && (
          <>
            <div class="sheet-running">
              <svg class="spin" viewBox="0 0 20 20" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true">
                <circle cx="10" cy="10" r="8" opacity="0.2" />
                <path d="M 10 2 a 8 8 0 0 1 8 8" />
              </svg>
              <span>выполняем на роутере…</span>
            </div>
            {sleepNote && <p class="sheet-note">{sleepNote}</p>}
          </>
        )}

        {phase === 'done' && (
          <div class="sheet-result">
            <p class={`state${result.status === 'ok' ? '' : ' state-error'}`}>
              <Quoted text={maintenanceOutcomeLabel(sheet.action, result, sheet.args) || commandOutcomeLabel(sheet.action, result)} />
            </p>
            <button type="button" class="btn btn-primary" onClick={onClose}>Закрыть</button>
          </div>
        )}

        {phase === 'error' && (
          <div class="sheet-result">
            {/* errorCode приходит только от ApiError (api.js): сервер отказал
                по коду, но для него ещё нет своей русской фразы. error там --
                `${path} failed: ${status}`, инженерный текст для сети, не для
                человека. Без кода (например таймаут ожидания) error уже несёт
                готовую русскую фразу -- её и показываем. */}
            <p class="state state-error">{commandErrorText(errorCode) || (errorCode ? 'Команда не отправлена' : error)}</p>
            <button type="button" class="btn btn-primary" onClick={onClose}>Закрыть</button>
          </div>
        )}
      </div>
    </div>
  )
}

// SheetHost -- лист поверх экрана, как его ставит App.jsx. key -- номер
// экземпляра из navReducer: новый лист монтируется с чистыми полями, даже
// если прежний не закрывали.
export function SheetHost({ nav, dispatch }) {
  if (!nav?.sheet) return null
  return (
    <Sheet
      key={nav.sheetSeq ?? 0}
      sheet={nav.sheet}
      asleep={nav.sheet.asleep}
      onClose={() => dispatch({ type: 'sheet', sheet: null })}
    />
  )
}
