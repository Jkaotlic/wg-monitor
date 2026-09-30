import { agentReplyText } from '../errorText.js'
import { useEffect, useState } from 'preact/hooks'
import { fetchRouterFacts } from '../api.js'
import { useCommand } from '../useCommand.js'
import { exitLine, wanView, hooksRow, nativeDNSView, logsView, pingFailsLine, SIGNAL_TEXTS } from '../signals.js'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'

// Факты v0.47 живут своим запросом: не ответил -- секции молчат, а экран
// остаётся рабочим (тот же приём, что у версий в SettingsScreen).
export function useFacts(routerID) {
  const [facts, setFacts] = useState(null)
  const load = () =>
    fetchRouterFacts(routerID)
      .then(setFacts)
      .catch(() => setFacts(null))
  useEffect(() => {
    setFacts(null)
    load()
  }, [routerID])
  return { facts, reload: load }
}

// Строки экрана VPN-туннеля: куда выходит трафик и сколько раз за сутки
// awg-manager не достучался.
export function ExitRow({ routerID, tunnelID, running = true }) {
  const { facts } = useFacts(routerID)
  if (!facts) return null
  const line = exitLine(facts, tunnelID, { running })
  const fails = pingFailsLine(facts, tunnelID)
  return (
    <>
      <DataRow title="Куда выходит трафик" value={line.value} valueSub={line.sub} valueTone={line.tone} />
      {line.warn && <p class="card-foot card-foot-bad">{line.warn}</p>}
      {fails && <DataRow title={fails.title} value={fails.value} valueTone={fails.tone} />}
    </>
  )
}

// «Проверки»: адрес выхода каждого VPN-туннеля и замер по кнопке.
export function ExitIPSection({ routerID, tunnels = [], deadline }) {
  const { facts, reload } = useFacts(routerID)
  const probe = useCommand(routerID)
  const [probing, setProbing] = useState('')
  if (!facts?.supported || !facts.exit) return null
  const list = tunnels.filter((t) => t.run_state === 'running' || facts.exit.tunnels?.[t.tunnel_id])
  if (!list.length) return null
  const run = (id) => {
    setProbing(id)
    probe.run('exit_ip_probe', { tunnel_id: id }, deadline).then(() => {
      setProbing('')
      reload()
    })
  }
  return (
    <Section title="Каким адресом видно каждый VPN-туннель">
      <div class="card card-rows">
        {list.map((t) => {
          const running = t.run_state === 'running'
          const line = exitLine(facts, t.tunnel_id, { running })
          return (
            // Предупреждение -- под строкой, а не третьей ячейкой её ряда:
            // в .settings-row (flex без переноса) оно выталкивало ряд за край.
            <div key={t.tunnel_id} class="data-row-group">
              <div class="settings-row">
                <DataRow title={`«${t.name || t.tunnel_id}»`} value={line.value} valueSub={line.sub} valueTone={line.tone} />
                {running && (
                  <button type="button" class="btn btn-ghost btn-row settings-row-btn" disabled={probe.busy} onClick={() => run(t.tunnel_id)}>
                    {probing === t.tunnel_id ? 'Меряем…' : 'Проверить сейчас'}
                  </button>
                )}
              </div>
              {line.warn && <p class="diag-consequence">{line.warn}</p>}
            </div>
          )
        })}
        {facts.exit.stale && <p class="card-foot">{SIGNAL_TEXTS.stale}</p>}
      </div>
      {probe.error && <p class="state state-error">{probe.error}</p>}
    </Section>
  )
}

export function WANSection({ routerID }) {
  const { facts } = useFacts(routerID)
  const view = wanView(facts)
  if (!view) return null
  return (
    <Section title="Резервный интернет">
      <div class="card card-rows">
        {view.rows.map((r) => (
          <DataRow key={r.key} title={r.title} value={r.value} valueSub={r.sub} valueTone={r.tone} />
        ))}
        {view.note && <p class="card-foot">{view.note}</p>}
        {view.hint && <p class="card-foot card-foot-bad">{view.hint}</p>}
      </div>
    </Section>
  )
}

export function HooksRow({ routerID }) {
  const { facts } = useFacts(routerID)
  if (!facts) return null
  const r = hooksRow(facts)
  return <DataRow title={r.title} value={r.value} valueTone={r.tone} />
}

const LOG_LEVELS = [
  ['error', 'только ошибки'],
  ['warn', 'предупреждения'],
  ['info', 'всё'],
]

// «Управление» → «Проверить»: журнал awg-manager по кнопке. Круг -- владелец и
// админ; решает SettingsSections (сервер отказывает оператору сам).
export function AwgmLogsSection({ routerID, deadline }) {
  const logs = useCommand(routerID)
  const [level, setLevel] = useState('')
  const view = logsView(logs.result)
  const load = (lv) => {
    setLevel(lv)
    logs.run('awgm_logs', { level: lv, limit: 100 }, deadline)
  }
  return (
    <Section title="Журнал awg-manager">
      <div class="filter-row">
        {LOG_LEVELS.map(([lv, label]) => (
          <button key={lv} type="button" class={`filter-chip${level === lv ? ' filter-chip-active' : ''}`} disabled={logs.busy} onClick={() => load(lv)}>
            {label}
          </button>
        ))}
      </div>
      {logs.busy && <p class="state">Читаем журнал…</p>}
      {logs.error && <p class="state state-error">{logs.error}</p>}
      {logs.result && logs.result.status !== 'ok' && <p class="state state-error">{agentReplyText(logs.result, 'Роутер не отдал журнал — попробуйте ещё раз через минуту.')}</p>}
      {view?.note && <p class="hint">{view.note}</p>}
      {view && view.rows.length > 0 && (
        <div class="card card-rows settings-card">
          {view.rows.map((r) => (
            <DataRow key={r.key} title={r.title} value={r.value} valueSub={r.sub} valueTone={r.tone} />
          ))}
        </div>
      )}
    </Section>
  )
}

export function NativeDNSSection({ routerID, tunnels = [] }) {
  const { facts } = useFacts(routerID)
  const view = nativeDNSView(facts, tunnels)
  if (!view) return null
  return (
    <Section title="Списки сайтов в самой прошивке">
      <div class="card card-rows">
        {view.rows.map((r) => (
          <div key={r.key} class="data-row-group">
            <DataRow title={r.title} value={r.value} valueSub={r.sub} valueTone={r.tone} />
            {r.warn && <p class="diag-consequence">{r.warn}</p>}
          </div>
        ))}
        {view.note && <p class="card-foot">{view.note}</p>}
      </div>
    </Section>
  )
}
