import { useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { sheetPhase, confirmReady } from '../sheet.js'
import { commandOutcomeLabel } from '../labels.js'
import { Q, Quoted } from './Q.jsx'

// Нижний шит -- единственное место, где приложение спрашивает "точно?" и
// показывает ход выполнения. Команду он и запускает сам: раньше это жило
// внутри кнопки, и каждая кнопка изобретала своё подтверждение заново.
//
// asleep приходит от экрана: роутер, который сейчас спит, отвечает не сразу,
// и обещать быстрый ответ было бы враньём -- поэтому и текст другой, и
// дедлайн ожидания шире.
export function Sheet({ sheet, asleep, onClose }) {
  const { busy, result, error, run } = useCommand(sheet.routerID)
  // Локальное действие (sheet.perform) выполняет сам бэкенд, а не роутер:
  // ходу выполнения там неоткуда взяться, поэтому фаза остаётся «спросить»,
  // а кнопка на время запроса гаснет.
  const local = typeof sheet.perform === 'function'
  const [localBusy, setLocalBusy] = useState(false)
  const [localError, setLocalError] = useState(null)
  const phase = local
    ? sheetPhase({ busy: false, result: null, error: localError })
    : sheetPhase({ busy, result, error })
  // Набранное подтверждение живёт здесь, а не в описании шита: описание --
  // это то, что задумал экран, а набранное -- то, что делает человек прямо
  // сейчас, и смешивать их значило бы переписывать намерение вводом.
  const [typed, setTyped] = useState('')
  const ready = confirmReady(sheet, typed)

  function start() {
    if (local) {
      setLocalBusy(true)
      setLocalError(null)
      Promise.resolve(sheet.perform())
        .then(() => {
          if (sheet.onDone) sheet.onDone()
          onClose()
        })
        .catch(() => setLocalError('Не получилось. Попробуйте ещё раз.'))
        .finally(() => setLocalBusy(false))
      return
    }
    run(sheet.action, sheet.args, { deadlineMs: asleep ? 6 * 60_000 : 90_000 }).then((res) => {
      if (res?.status === 'ok' && sheet.onDone) sheet.onDone()
    })
  }

  return (
    <div class="sheet-layer">
      {/* Подложка закрывает шит только до запуска: обрывать наблюдение за
          уже ушедшей на роутер командой случайным тапом мимо -- плохая идея. */}
      <div class="sheet-scrim" onClick={phase === 'running' ? undefined : onClose} />
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
              <p class="sheet-note">Роутер сейчас не на связи. Команда выполнится, когда он проснётся.</p>
            )}
            {/* Локальное действие роутеру не уходит -- показывать имя команды
                нечего, и строка «команда: undefined» была бы враньём. */}
            {!local && (
              <div class="sheet-command">
                <span class="sheet-command-label">команда</span>
                <span class="sheet-command-value">{sheet.action}</span>
              </div>
            )}
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
              <button type="button" class="btn btn-ghost" onClick={onClose}>Отмена</button>
              <button
                type="button"
                class={`btn ${sheet.danger ? 'btn-danger' : 'btn-primary'}`}
                disabled={!ready || localBusy}
                onClick={start}
              >
                {localBusy ? 'Сохраняем…' : sheet.buttonLabel}
              </button>
            </div>
          </>
        )}

        {phase === 'running' && (
          <div class="sheet-running">
            <svg class="spin" viewBox="0 0 20 20" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true">
              <circle cx="10" cy="10" r="8" opacity="0.2" />
              <path d="M 10 2 a 8 8 0 0 1 8 8" />
            </svg>
            <span>выполняем на роутере…</span>
          </div>
        )}

        {phase === 'done' && (
          <div class="sheet-result">
            <p class={`state${result.status === 'ok' ? '' : ' state-error'}`}>
              <Quoted text={commandOutcomeLabel(sheet.action, result)} />
            </p>
            <button type="button" class="btn btn-primary" onClick={onClose}>Закрыть</button>
          </div>
        )}

        {phase === 'error' && (
          <div class="sheet-result">
            <p class="state state-error">{error}</p>
            <button type="button" class="btn btn-primary" onClick={onClose}>Закрыть</button>
          </div>
        )}
      </div>
    </div>
  )
}
