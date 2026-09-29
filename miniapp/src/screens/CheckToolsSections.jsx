import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings, fetchRouterChecks } from '../api.js'
import { useCommand } from '../useCommand.js'
import { doctorRows, pingRows } from '../settings.js'
import { confirmSheet } from '../sheet.js'
import { agentAtLeast } from '../agentConfig.js'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { ErrorLine } from '../ui/ErrorLine.jsx'
import { AwgmLogsSection } from './SignalSections.jsx'

// «Проверка связи», «Осмотр» и журнал awg-manager (v0.50, спека п. 3.1):
// раньше -- группа «Проверить» в «Управлении», теперь -- на вкладке
// «Проверки», где роутер и спрашивают. Права прежние: переключатель
// проверки связи -- листом (решает сервер), журнал -- владельцу и админу с
// агентом v0.47+ (оператору сервер откажет сам).
export function CheckToolsSections({ routerID, asleep, openSheet }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [showRaw, setShowRaw] = useState(false)
  const doctor = useCommand(routerID)
  const hrneo = useCommand(routerID)
  const pingNow = useCommand(routerID)

  function load() {
    return Promise.all([fetchRouterSettings(routerID), fetchRouterChecks(routerID)])
      .then(([s, c]) => {
        setSettings(s)
        setTunnels(c.tunnels ?? [])
      })
      .catch(() => {})
  }

  useEffect(() => {
    setSettings(null)
    setTunnels([])
    load()
  }, [routerID])

  const doctorOut = doctor.result?.status === 'ok' ? doctorRows(doctor.result.output) : []
  const hrneoOut = hrneo.result?.status === 'ok' ? doctorRows(hrneo.result.output) : []
  const pings = pingRows(tunnels)
  const runPing = () => pingNow.run('pingcheck_now', {}, deadline).then((res) => { if (res?.status === 'ok') load() })

  const askPingToggle = (row) => {
    openSheet(
      confirmSheet({
        routerID,
        title: row.enabled ? `Выключить проверку связи у «${row.title}»?` : `Включить проверку связи у «${row.title}»?`,
        body: row.enabled
          ? 'Роутер перестанет сам проверять этот VPN-туннель и поднимать его. Тревога о падении по-прежнему придёт — по обмену ключами.'
          : 'Роутер начнёт сам проверять VPN-туннель и поднимать его, если ответа не будет.',
        action: 'pingcheck_toggle',
        args: { tunnel_id: row.tunnelID, enable: !row.enabled },
        buttonLabel: row.enabled ? 'Выключить' : 'Включить',
        danger: Boolean(row.enabled),
        asleep,
        onDone: load,
      }),
    )
  }

  return (
    <>
      <Section title="Проверка связи">
        {pings.length === 0 ? (
          <div class="card">
            <p class="traffic-detail">Роутер не сообщил ни одного VPN-туннеля.</p>
          </div>
        ) : (
          <div class="card card-rows">
            {pings.map((r) => (
              <div key={r.key} class="settings-row">
                <DataRow dot={r.tone === 'muted' ? undefined : r.tone} title={r.title} code={r.code} value={r.value} valueTone={r.tone === 'muted' ? undefined : r.tone} />
                {r.enabled != null && openSheet && (
                  <button type="button" class="btn btn-ghost btn-row settings-row-btn" onClick={() => askPingToggle(r)}>
                    {r.enabled ? 'Выключить' : 'Включить'}
                  </button>
                )}
              </div>
            ))}
            <p class="card-foot">Роутер сам проверяет VPN-туннель и поднимает его, если ответа нет. Задержка — это то, что он намерил последним замером.</p>
          </div>
        )}
        <button type="button" class="btn btn-ghost btn-wide" disabled={pingNow.busy} onClick={runPing}>
          {pingNow.busy ? 'Проверяем…' : 'Проверить связь сейчас'}
        </button>
        <ErrorLine text={pingNow.error} busy={pingNow.busy} onRetry={runPing} />
      </Section>

      <Section title="Проверить роутер изнутри">
        <div class="action-row">
          <button type="button" class="btn btn-ghost" disabled={doctor.busy} onClick={() => doctor.run('router_doctor', {}, deadline)}>
            {doctor.busy ? 'Смотрим…' : 'Осмотр роутера'}
          </button>
          <button type="button" class="btn btn-ghost" disabled={hrneo.busy} onClick={() => hrneo.run('hrneo_doctor', {}, deadline)}>
            {hrneo.busy ? 'Смотрим…' : 'Осмотр HydraRoute Neo'}
          </button>
        </div>
        <ErrorLine text={doctor.error || hrneo.error} />
        {[...doctorOut, ...hrneoOut].length > 0 && (
          <div class="card card-rows settings-card">
            {[...doctorOut, ...hrneoOut].map((r, i) => (
              <DataRow key={`${r.key}-${i}`} dot={r.tone} title={r.title} value={r.value} valueTone={r.tone} />
            ))}
          </div>
        )}
        {(doctor.result?.status === 'ok' || hrneo.result?.status === 'ok') && (
          <>
            <button type="button" class="btn btn-ghost raw-toggle" onClick={() => setShowRaw((v) => !v)}>
              {showRaw ? 'Скрыть ответ целиком' : 'Ответ роутера целиком'}
            </button>
            {showRaw && <pre class="raw-dump">{[doctor.result?.output, hrneo.result?.output].filter(Boolean).join('\n\n')}</pre>}
          </>
        )}
      </Section>

      {settings && settings.role !== 'operator' && agentAtLeast(settings.agent_version, 'v0.47.0') && <AwgmLogsSection routerID={routerID} deadline={deadline} />}
    </>
  )
}
