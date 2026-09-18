import { useEffect, useState } from 'preact/hooks'
import { fetchRouterChecks, fetchRouterSettings } from '../api.js'
import { useCommand } from '../useCommand.js'
// Помощник копирования части 2 (тот же, что у экрана токена).
import { copyText } from '../clipboard.js'
import { confirmSheet } from '../sheet.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import {
  dnsResetAvailable,
  dnsResetConfirmBody,
  dnsResetScreenTexts,
  dnsReferenceCommands,
  doneText,
  parsePreview,
  parseReset,
  postconditionRows,
  previewText,
  resetEnabled,
} from '../dnsReset.js'

const T = dnsResetScreenTexts()

// «Сброс DNS» -- экран, перенесённый из операторского дашборда (решение
// оператора № 5). Порядок экрана и есть его защита:
//
//   1. Роутеру с агентом ниже пола версии экран не рисует ни одной кнопки --
//      вторая преграда, независимая от отказа бэкенда.
//   2. Первая кнопка -- «посмотреть», а не «сбросить». Сброс открывается
//      только после предпросмотра и закрывается снова после каждого сброса.
//   3. Ответ, который не похож на предпросмотр, -- громкое предупреждение:
//      на «посмотреть» роутер ответил сбросом, значит что-то уже случилось.
//   4. После сброса -- путь к снимку «до» и три постусловия по свежему отчёту.
export function DNSResetScreen({ routerID, routerName, asleep, openSheet, onClose }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [before, setBefore] = useState([])
  const [after, setAfter] = useState(null)
  const [previewFresh, setPreviewFresh] = useState(false)
  const [reset, setReset] = useState(null)
  // '' -- ещё не копировали, 'ok' / 'fail' -- итог последнего нажатия.
  const [copyState, setCopyState] = useState('')

  function copyCommands() {
    copyText(dnsReferenceCommands()).then((ok) => setCopyState(ok ? 'ok' : 'fail'))
  }
  const preview = useCommand(routerID)
  const recheck = useCommand(routerID)

  useEffect(() => {
    Promise.all([fetchRouterSettings(routerID), fetchRouterChecks(routerID)])
      .then(([s, c]) => {
        setSettings(s)
        setBefore(c?.checks ?? [])
        setLoadError(null)
      })
      .catch(() => setLoadError('Не удалось прочитать настройки роутера.'))
  }, [routerID])

  const available = dnsResetAvailable(settings)
  const parsed = preview.result?.status === 'ok' ? parsePreview(preview.result.output) : null
  const notAPreview = preview.result?.status === 'ok' && !parsed

  function askPreview() {
    setPreviewFresh(false)
    preview.run('dns_reset', { dry_run: true }, deadline).then((res) => {
      setPreviewFresh(res?.status === 'ok' && parsePreview(res.output) != null)
    })
  }

  function loadAfter() {
    recheck.run('force_recheck', {}, deadline).then(() =>
      fetchRouterChecks(routerID)
        .then((c) => setAfter(c?.checks ?? []))
        .catch(() => {}),
    )
  }

  function askReset() {
    if (!routerName) return
    // Кнопка закрывается в момент отправки, а не по приходу ответа: ответа
    // может не быть вовсе (таймаут, спящий роутер), а команда уже в очереди.
    // Отмена в шите тоже потребует нового предпросмотра -- для самого опасного
    // действия цикла это правильная цена.
    setPreviewFresh(false)
    openSheet(
      confirmSheet({
        routerID,
        title: `Сбросить DNS на «${routerName}»?`,
        body: dnsResetConfirmBody(routerName),
        action: 'dns_reset',
        args: { dry_run: false },
        buttonLabel: 'Сбросить DNS',
        danger: true,
        asleep,
        confirmPhrase: routerName || '',
        onResult: (res) => {
          setReset(parseReset(res))
          setAfter(null)
          if (res?.status !== 'err') loadAfter()
        },
      }),
    )
  }

  return (
    <Overlay title="Сброс DNS" backLabel="Управление" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">{routerName || 'Роутер'}</h1>
        {loadError && <p class="state state-error">{loadError}</p>}
        {settings && settings.role !== 'admin' && <p class="hint">{T.adminOnly}</p>}
        {settings && settings.role === 'admin' && !available && (
          <p class="hint">
            {T.tooOld} Агент на роутере: {settings.agent_version || 'версию не сообщал'}.
          </p>
        )}

        {available && (
          <>
            <Section title="Что изменится">
              <p class="hint">{T.intro}</p>
              <button type="button" class="btn btn-ghost btn-wide" disabled={preview.busy} onClick={askPreview}>
                {preview.busy ? 'Спрашиваем роутер…' : T.previewButton}
              </button>
              {preview.error && <p class="state state-error">{preview.error}</p>}
              {preview.result && preview.result.status !== 'ok' && (
                <p class="state state-error">Роутер не показал предпросмотр: {preview.result.output || preview.result.status}</p>
              )}
              {notAPreview && (
                <p class="state state-error">
                  Роутер ответил не предпросмотром — возможно, настройки DNS уже изменены. Сбрасывать не нужно: откройте
                  «Проверки» и посмотрите раздел «Раздельный DNS».
                </p>
              )}
              {parsed && (
                <div class="card">
                  <p class="traffic-title">{previewText(parsed)}</p>
                  {parsed.remove.length > 0 && <pre class="raw-dump">{parsed.remove.map((l) => `− ${l}`).join('\n')}</pre>}
                  {parsed.keep.length > 0 && <pre class="raw-dump">{parsed.keep.map((l) => `= ${l}`).join('\n')}</pre>}
                </div>
              )}
            </Section>

            <Section title="Сброс">
              <button
                type="button"
                class="btn btn-danger btn-wide"
                disabled={!routerName || !resetEnabled({ previewed: previewFresh })}
                onClick={askReset}
              >
                {T.resetButton}
              </button>
              {!previewFresh && <p class="hint">{T.previewFirst}</p>}
            </Section>

            {reset && (
              <Section title="После сброса">
                <p class={`state${reset.status === 'ok' && reset.snapshot ? '' : ' state-error'}`}>{doneText(reset)}</p>
                {reset.status !== 'err' && (
                  <div class="card">
                    {postconditionRows({ before, after: after ?? before }).map((r) => (
                      <DataRow
                        key={r.key}
                        dot={r.tone === 'muted' ? undefined : r.tone}
                        title={r.title}
                        value={r.value}
                        valueTone={r.tone === 'ok' ? undefined : r.tone}
                      />
                    ))}
                  </div>
                )}
                {reset.status !== 'err' && (
                  <button type="button" class="btn btn-ghost btn-wide" disabled={recheck.busy} onClick={loadAfter}>
                    {recheck.busy ? 'Спрашиваем роутер…' : T.checkAgain}
                  </button>
                )}
              </Section>
            )}
          </>
        )}
        {/* Ручной прогон нужен и тогда, когда кнопки сброса нет (старый
            агент): команды не зависят от версии агента. Только админу --
            как и весь экран. */}
        {settings && settings.role === 'admin' && (
          <Section title={T.manualTitle}>
            <p class="hint">{T.manualIntro}</p>
            <pre class="raw-dump dns-commands">{dnsReferenceCommands()}</pre>
            <button type="button" class="btn btn-ghost btn-wide dns-copy" onClick={copyCommands}>
              {T.copyButton}
            </button>
            {copyState === 'ok' && <p class="result-note">{T.copied}</p>}
            {copyState === 'fail' && <p class="result-note result-note-error">{T.copyFailed}</p>}
          </Section>
        )}
      </div>
    </Overlay>
  )
}
