import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings } from '../api.js'
import { agentGateNote } from '../agentConfig.js'
import { useCommand } from '../useCommand.js'
import { commandErrorText } from '../maintenance.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { PackagesCard } from './PackagesCard.jsx'
import {
  SPACE_TEXTS as T,
  cleanOutcomeText,
  parseSpaceReport,
  spaceAvailable,
  spaceDeadlineMs,
  spaceFailureText,
  spaceSummaryRows,
  spaceTopRows,
} from '../space.js'

function CommandError({ cmd }) {
  if (!cmd.error) return null
  return <p class="state state-error">{commandErrorText(cmd.errorCode) || (cmd.errorCode ? 'Команда не отправлена.' : cmd.error)}</p>
}

// «Свободное место» (v0.57) -- экран «Обслуживания», только админ (сервер
// отвечает остальным 404). Отчёт о месте и «Почистить сейчас» -- агенту от
// v0.57 (space_report новый, а очистка без расписания раньше падала);
// карточка расписания очистки переехала сюда из «Пакетов по расписанию» и
// работает с любым агентом -- старому она оставляет свою «Запустить сейчас».
export function SpaceScreen({ routerID, routerName, asleep, onClose }) {
  const reportCmd = useCommand(routerID)
  const cleanCmd = useCommand(routerID)
  const [settings, setSettings] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [report, setReport] = useState(null)
  // Итог последней очистки: ответ очистки и место до и после.
  const [clean, setClean] = useState(null)

  const available = spaceAvailable(settings)
  const isAdmin = settings?.role === 'admin'
  const deadline = (action) => ({ deadlineMs: spaceDeadlineMs(action, asleep) })

  function measure() {
    return reportCmd.run('space_report', {}, deadline('space_report')).then((res) => {
      const parsed = parseSpaceReport(res)
      if (parsed) setReport(parsed)
      return parsed
    })
  }

  function runClean() {
    if (cleanCmd.busy || reportCmd.busy) return
    const before = report
    setClean(null)
    cleanCmd.run('entware_clean_run', {}, deadline('entware_clean_run')).then((res) => {
      if (!res) return
      if (res.status !== 'ok') {
        setClean({ run: res, before, after: null })
        return
      }
      measure().then((after) => setClean({ run: res, before, after }))
    })
  }

  useEffect(() => {
    let alive = true
    fetchRouterSettings(routerID)
      .then((s) => {
        if (!alive) return
        setSettings(s)
        setLoadError(null)
        // Спящему роутеру на входе ничего не шлём: вопрос повис бы на минуты.
        if (spaceAvailable(s) && !asleep) measure()
      })
      .catch(() => alive && setLoadError('Не удалось прочитать настройки роутера.'))
    return () => {
      alive = false
    }
  }, [routerID])

  const reportFail = spaceFailureText(reportCmd.result)
  const top = spaceTopRows(report)
  const outcome = clean ? cleanOutcomeText(clean) : ''
  const cleanFailed = clean && clean.run.status !== 'ok'

  return (
    <Overlay title={T.title} backLabel="Настройки" onBack={onClose}>
      <div class="screen space-screen">
        <h1 class="screen-title">{T.title}</h1>
        {routerName && <p class="router-lastseen">{routerName}</p>}
        <p class="hint">{T.about}</p>
        {loadError && <p class="state state-error">{loadError}</p>}
        {settings && !isAdmin && <p class="hint">{T.adminOnly}</p>}
        {asleep && isAdmin && <p class="hint">Роутер сейчас не на связи — команды подождут его несколько минут.</p>}
        {isAdmin && !available && <p class="hint">{agentGateNote(settings.agent_version, T.tooOld)}</p>}

        {available && (
          <Section title="Накопитель">
            <div class="card">
              {report ? (
                spaceSummaryRows(report).map((r) => <DataRow key={r.key} title={r.title} value={r.value} valueTone={r.tone} />)
              ) : (
                <p class="state">{reportCmd.busy ? 'Считаем место…' : T.unknown}</p>
              )}
              {report && (
                <>
                  <p class="access-subtitle v057-subtitle">{T.topTitle}</p>
                  {top.length > 0 ? (
                    top.map((r) => <DataRow key={r.key} title={r.title} value={r.value} />)
                  ) : (
                    <p class="hint space-top-unknown">{T.topUnknown}</p>
                  )}
                </>
              )}
              {reportFail && <p class="result-note result-note-error">{reportFail}</p>}
              <CommandError cmd={reportCmd} />
              {reportCmd.result && reportCmd.result.status !== 'ok' && reportCmd.result.output && (
                <details class="packages-details">
                  <summary>Подробности</summary>
                  <pre class="raw-dump">{reportCmd.result.output}</pre>
                </details>
              )}
              <div class="packages-actions action-row">
                <button type="button" class="btn btn-ghost btn-row" disabled={reportCmd.busy || cleanCmd.busy} onClick={measure}>
                  Проверить
                </button>
              </div>
            </div>
          </Section>
        )}

        {available && (
          <Section title={T.cleanTitle}>
            <div class="card">
              <p class="hint">{T.cleanAbout}</p>
              <button type="button" class="btn btn-primary btn-wide space-clean" disabled={cleanCmd.busy || reportCmd.busy} onClick={runClean}>
                {cleanCmd.busy ? 'Чистим…' : T.cleanButton}
              </button>
              {cleanCmd.sleepNote && <p class="hint">{cleanCmd.sleepNote}</p>}
              {outcome && <p class={`result-note${cleanFailed ? ' result-note-error' : ''}`}>{outcome}</p>}
              <CommandError cmd={cleanCmd} />
              {cleanFailed && clean.run.output && (
                <details class="packages-details">
                  <summary>Подробности</summary>
                  <pre class="raw-dump">{clean.run.output}</pre>
                </details>
              )}
            </div>
          </Section>
        )}

        {isAdmin && <PackagesCard kind="clean" routerID={routerID} asleep={asleep} canRun={!available} />}
      </div>
    </Overlay>
  )
}
