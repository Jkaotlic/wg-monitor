import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings } from '../api.js'
import { agentGateNote } from '../agentConfig.js'
import { useCommand } from '../useCommand.js'
import { commandErrorText } from '../maintenance.js'
import { recentRouteSnapshot } from '../routes.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import {
  PORTHOP_TEXTS as T,
  parsePorthopStatus,
  porthopAction,
  porthopArgs,
  porthopAvailable,
  porthopBusyText,
  porthopButtons,
  porthopDeadlineMs,
  porthopFailure,
  porthopOutcomeText,
  porthopStatusRows,
} from '../porthop.js'

// «Смена порта при блокировке» (v0.57) -- экран «Обслуживания», только
// админ: вход рисуется только ему, сервер отвечает остальным 404 и отказывает
// агенту старше v0.57 (agent_too_old). Экран и сам не рисует кнопок старому
// агенту -- вторая преграда, независимая от бэкенда.
//
// Имена VPN-туннелей -- из недавнего снимка вкладки «VPN-туннели»; своего
// route_status экран не шлёт (лишняя команда роутеру ради подписи).
export function PorthopScreen({ routerID, routerName, asleep, onClose }) {
  const cmd = useCommand(routerID)
  const [settings, setSettings] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [status, setStatus] = useState(null)
  const [verb, setVerb] = useState('')
  const [log, setLog] = useState(null)
  const [failure, setFailure] = useState(null)

  const available = porthopAvailable(settings)

  function run(next) {
    if (cmd.busy) return
    setVerb(next)
    cmd.run(porthopAction(next), porthopArgs(next), { deadlineMs: porthopDeadlineMs(next, asleep) }).then((res) => {
      if (!res) return
      const fail = porthopFailure(res)
      // Отказ «есть ручная копия» помнится до следующего удачного ответа:
      // по нему экран держит кнопку замены.
      if (fail?.kind === 'legacy') setFailure(fail)
      const parsed = parsePorthopStatus(res)
      if (!parsed) return
      setFailure(null)
      setStatus(parsed)
      if (next === 'logs') setLog(parsed.logTail)
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
        if (porthopAvailable(s) && !asleep) run('status')
      })
      .catch(() => alive && setLoadError('Не удалось прочитать настройки роутера.'))
    return () => {
      alive = false
    }
  }, [routerID])

  const buttons = porthopButtons(status, failure)
  const outcome = porthopOutcomeText(verb, cmd.result)
  const failed = cmd.result && cmd.result.status !== 'ok'
  const legacyFail = porthopFailure(cmd.result)?.kind === 'legacy'
  const snapshot = recentRouteSnapshot(routerID)

  return (
    <Overlay title={T.title} backLabel="Настройки" onBack={onClose}>
      <div class="screen porthop-screen">
        <h1 class="screen-title">{T.title}</h1>
        {routerName && <p class="router-lastseen">{routerName}</p>}
        <p class="hint">{T.about}</p>
        {loadError && <p class="state state-error">{loadError}</p>}
        {settings && settings.role !== 'admin' && <p class="hint">{T.adminOnly}</p>}
        {settings && settings.role === 'admin' && !available && <p class="hint">{agentGateNote(settings.agent_version, T.tooOld)}</p>}

        {available && (
          <Section title="Состояние">
            <div class="card">
              {asleep && <p class="hint">Роутер сейчас не на связи — команды подождут его несколько минут.</p>}
              {status ? (
                porthopStatusRows(status, snapshot).map((r) => <DataRow key={r.key} title={r.title} value={r.value} valueTone={r.tone} />)
              ) : (
                <p class="state">{cmd.busy ? porthopBusyText('status') : T.unknown}</p>
              )}
              <p class="hint">{T.limits}</p>

              <div class="packages-actions action-row">
                {buttons.replace && (
                  <button type="button" class="btn btn-primary btn-row porthop-replace" disabled={cmd.busy} onClick={() => run('replace')}>
                    Заменить ручную копию
                  </button>
                )}
                {buttons.install && (
                  <button type="button" class="btn btn-primary btn-row" disabled={cmd.busy} onClick={() => run('install')}>
                    {buttons.install}
                  </button>
                )}
                <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('logs')}>
                  Журнал
                </button>
                <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('status')}>
                  Проверить
                </button>
                {buttons.remove && (
                  <button type="button" class="btn btn-ghost btn-row" disabled={cmd.busy} onClick={() => run('remove')}>
                    Выключить
                  </button>
                )}
              </div>

              {cmd.busy && <p class="hint">{porthopBusyText(verb)}</p>}
              {cmd.sleepNote && <p class="hint">{cmd.sleepNote}</p>}
              {outcome && <p class={`result-note${failed ? ' result-note-error' : ''}`}>{outcome}</p>}
              {cmd.error && (
                <p class="state state-error">{commandErrorText(cmd.errorCode) || (cmd.errorCode ? 'Команда не отправлена.' : cmd.error)}</p>
              )}
              {failed && !legacyFail && cmd.result.output && (
                <details class="packages-details">
                  <summary>Подробности</summary>
                  <pre class="raw-dump">{cmd.result.output}</pre>
                </details>
              )}
              {log !== null && (log ? <pre class="raw-dump packages-log">{log}</pre> : <p class="hint">Журнал пуст.</p>)}
              {status?.logPath && <p class="hint">Журнал на роутере: {status.logPath}</p>}
            </div>
          </Section>
        )}
      </div>
    </Overlay>
  )
}
