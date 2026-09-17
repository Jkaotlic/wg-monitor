import { useEffect, useRef, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { commandErrorText } from '../maintenance.js'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import {
  PACKAGE_JOBS,
  normalizeHHMM,
  packagesAction,
  packagesArgs,
  packagesBusyText,
  packagesDeadlineMs,
  packagesOutcomeText,
  packagesStatusRows,
  parsePackagesStatus,
} from '../packagesSchedule.js'

// Одна карточка расписания: состояние, время, действия, итог, журнал.
// Любой ответ агента несёт свежий статус -- карточка его и рисует.
export function PackagesCard({ kind, routerID, asleep }) {
  const job = PACKAGE_JOBS[kind]
  const cmd = useCommand(routerID)
  const [status, setStatus] = useState(null)
  const [time, setTime] = useState(job.defaultTime)
  const [verb, setVerb] = useState('')
  const [log, setLog] = useState(null)
  // Время, которое человек уже трогал, ответ роутера не перетирает.
  const touched = useRef(false)

  function run(next) {
    const action = packagesAction(kind, next)
    if (!action || cmd.busy) return
    if (next === 'install' && !normalizeHHMM(time)) return
    setVerb(next)
    cmd.run(action, packagesArgs(kind, next, time), { deadlineMs: packagesDeadlineMs(next, asleep) }).then((res) => {
      const parsed = parsePackagesStatus(res)
      if (!parsed) return
      setStatus(parsed)
      if (next === 'logs') setLog(parsed.logTail)
      if (!touched.current && parsed.time) setTime(parsed.time)
    })
  }

  // Спящему роутеру на входе ничего не шлём: вопрос повис бы на минуты, а
  // человек пришёл, может быть, только посмотреть.
  useEffect(() => {
    if (!asleep) run('status')
  }, [routerID])

  const timeOK = normalizeHHMM(time) !== ''
  const outcome = packagesOutcomeText(kind, verb, cmd.result)
  const failed = cmd.result && cmd.result.status !== 'ok'

  return (
    <div class={`packages-card packages-card-${kind}`}>
      <Section title={job.title}>
        <div class="card">
          <p class="hint">{job.about}</p>
          {status ? (
            packagesStatusRows(status, kind).map((r) => <DataRow key={r.key} title={r.title} value={r.value} valueTone={r.tone} />)
          ) : (
            <p class="state">{cmd.busy ? packagesBusyText('status') : 'Состояние ещё не проверено.'}</p>
          )}

          <div class="field packages-time">
            <label for={`packages-time-${kind}`}>Время запуска, каждый день</label>
            <input
              id={`packages-time-${kind}`}
              type="time"
              autocomplete="off"
              value={time}
              onInput={(e) => {
                touched.current = true
                setTime(e.currentTarget.value)
              }}
            />
          </div>
          {!timeOK && <p class="state state-error">Время пишется так: {job.defaultTime}.</p>}
          {status?.installed && !status.time && (
            <p class="hint">Сейчас стоит своё расписание cron. «Изменить время» заменит его ежедневным запуском.</p>
          )}

          <div class="packages-actions">
            <button type="button" class="btn btn-primary btn-row" disabled={cmd.busy || !timeOK} onClick={() => run('install')}>
              {status?.installed ? 'Изменить время' : 'Включить'}
            </button>
            {job.canRun && (
              <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('run')}>
                Запустить сейчас
              </button>
            )}
            <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('logs')}>
              Журнал
            </button>
            <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('status')}>
              Проверить
            </button>
            {status?.installed && (
              <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('remove')}>
                Выключить
              </button>
            )}
          </div>

          {cmd.busy && <p class="hint">{packagesBusyText(verb)}</p>}
          {cmd.sleepNote && <p class="hint">{cmd.sleepNote}</p>}
          {outcome && <p class={`result-note${failed ? ' result-note-error' : ''}`}>{outcome}</p>}
          {cmd.error && (
            <p class="state state-error">{commandErrorText(cmd.errorCode) || (cmd.errorCode ? 'Команда не отправлена.' : cmd.error)}</p>
          )}
          {failed && cmd.result.output && (
            <details class="packages-details">
              <summary>Подробности</summary>
              <pre class="raw-dump">{cmd.result.output}</pre>
            </details>
          )}
          {log !== null && (log ? <pre class="raw-dump packages-log">{log}</pre> : <p class="hint">Журнал пуст.</p>)}
        </div>
      </Section>
    </div>
  )
}
