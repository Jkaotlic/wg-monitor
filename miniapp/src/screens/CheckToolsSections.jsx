import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings } from '../api.js'
import { useCommand } from '../useCommand.js'
import { doctorRows, pingRows } from '../settings.js'
import { confirmSheet } from '../sheet.js'
import { agentAtLeast } from '../agentConfig.js'
import { DataRow } from '../ui/DataRow.jsx'
import { ErrorLine } from '../ui/ErrorLine.jsx'
import { AwgmLogsSection } from './SignalSections.jsx'

// «Проверка связи VPN-туннелей» и «Осмотр изнутри» (v0.52, спека §2): два
// раздела вкладки «Проверки» → «Сейчас»; заголовки даёт сам раздел вкладки.
// Права прежние: переключатель проверки связи -- листом (решает сервер),
// журнал -- владельцу и админу с агентом v0.47+ (оператору сервер откажет сам).

// Переключатель -- «Следить / Не следить» (было «Выключить» -- путалось с
// выключением самого VPN-туннеля).
export function PingCheckSection({ routerID, asleep, openSheet, tunnels = [], onChanged }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const pingNow = useCommand(routerID)
  const pings = pingRows(tunnels)
  const runPing = () => pingNow.run('pingcheck_now', {}, deadline).then((res) => { if (res?.status === 'ok') onChanged?.() })

  const askPingToggle = (row) => {
    openSheet(
      confirmSheet({
        routerID,
        title: row.enabled ? `Перестать следить за «${row.title}»?` : `Следить за «${row.title}»?`,
        body: row.enabled
          ? 'Роутер перестанет сам проверять этот VPN-туннель и поднимать его. Тревога о падении по-прежнему придёт — по обмену ключами.'
          : 'Роутер начнёт сам проверять VPN-туннель и поднимать его, если ответа не будет.',
        action: 'pingcheck_toggle',
        args: { tunnel_id: row.tunnelID, enable: !row.enabled },
        buttonLabel: row.enabled ? 'Не следить' : 'Следить',
        danger: Boolean(row.enabled),
        asleep,
        onDone: onChanged,
      }),
    )
  }

  return (
    <>
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
                  {r.enabled ? 'Не следить' : 'Следить'}
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
    </>
  )
}

// «Осмотр изнутри»: осмотр роутера и HydraRoute Neo, отчёт (children -- его
// рисует вкладка, у неё команда diag_now), журнал awg-manager (владельцу и
// админу с агентом v0.47+; оператору сервер откажет сам).
export function InspectSection({ routerID, asleep, children }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [showRaw, setShowRaw] = useState(false)
  const doctor = useCommand(routerID)
  const hrneo = useCommand(routerID)

  useEffect(() => {
    setSettings(null)
    fetchRouterSettings(routerID).then(setSettings).catch(() => {})
  }, [routerID])

  const doctorOut = doctor.result?.status === 'ok' ? doctorRows(doctor.result.output) : []
  const hrneoOut = hrneo.result?.status === 'ok' ? doctorRows(hrneo.result.output) : []

  return (
    <>
      <div class="action-row">
        <button type="button" class="btn btn-ghost" disabled={doctor.busy} onClick={() => doctor.run('router_doctor', {}, deadline)}>
          {doctor.busy ? 'Смотрим…' : 'Осмотреть роутер'}
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
      {children}
      {settings && settings.role !== 'operator' && agentAtLeast(settings.agent_version, 'v0.47.0') && <AwgmLogsSection routerID={routerID} deadline={deadline} />}
    </>
  )
}
