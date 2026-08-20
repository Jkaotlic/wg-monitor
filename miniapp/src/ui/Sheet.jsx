import { useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { sheetPhase, confirmReady } from '../sheet.js'
import { commandOutcomeLabel } from '../labels.js'

// Нижний шит -- единственное место, где приложение спрашивает "точно?" и
// показывает ход выполнения. Команду он и запускает сам: раньше это жило
// внутри кнопки, и каждая кнопка изобретала своё подтверждение заново.
//
// asleep приходит от экрана: роутер, который сейчас спит, отвечает не сразу,
// и обещать быстрый ответ было бы враньём -- поэтому и текст другой, и
// дедлайн ожидания шире.
export function Sheet({ sheet, asleep, onClose }) {
  const { busy, result, error, run } = useCommand(sheet.routerID)
  const phase = sheetPhase({ busy, result, error })
  // Набранное подтверждение живёт здесь, а не в описании шита: описание --
  // это то, что задумал экран, а набранное -- то, что делает человек прямо
  // сейчас, и смешивать их значило бы переписывать намерение вводом.
  const [typed, setTyped] = useState('')
  const ready = confirmReady(sheet, typed)

  function start() {
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
        <p class="sheet-title">{sheet.title}</p>
        <p class="sheet-body">{sheet.body}</p>

        {phase === 'confirm' && (
          <>
            {asleep && (
              <p class="sheet-note">Роутер сейчас не на связи. Команда выполнится, когда он проснётся.</p>
            )}
            <div class="sheet-command">
              <span class="sheet-command-label">команда</span>
              <span class="sheet-command-value">{sheet.action}</span>
            </div>
            {sheet.confirmPhrase && (
              <div class="field sheet-confirm">
                <label for="sheet-confirm-input">Наберите «{sheet.confirmPhrase}», чтобы подтвердить</label>
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
                disabled={!ready}
                onClick={start}
              >
                {sheet.buttonLabel}
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
              {commandOutcomeLabel(sheet.action, result)}
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
