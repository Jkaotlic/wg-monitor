import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings, fetchRouterChecks } from '../api.js'
import { useCommand } from '../useCommand.js'
import { thresholdRows, auditRows, doctorRows, pingRows } from '../settings.js'
import { confirmSheet } from '../sheet.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'

// Настройки роутера и обслуживание -- то, за чем оператор раньше шёл в бота.
//
// Экран ничего не настраивает в самом приложении: настраивать там нечего.
// Он показывает, по каким правилам бот судит об этом роутере (числа живут в
// backend.yaml), что на роутере стоит из версий и что можно спросить у него
// прямо сейчас.
export function SettingsScreen({ routerID, routerName, asleep, openSheet, onClose }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [error, setError] = useState(null)
  const [showHelp, setShowHelp] = useState(false)

  const audit = useCommand(routerID)
  const doctor = useCommand(routerID)
  const hrneo = useCommand(routerID)
  const pingNow = useCommand(routerID)

  function load() {
    return Promise.all([fetchRouterSettings(routerID), fetchRouterChecks(routerID)])
      .then(([s, c]) => {
        setSettings(s)
        setTunnels(c.tunnels ?? [])
        setError(null)
      })
      .catch(() => setError('Не удалось прочитать настройки роутера.'))
  }

  useEffect(() => {
    load()
  }, [routerID])

  const auditOut = audit.result?.status === 'ok' ? auditRows(audit.result.output) : []
  const doctorOut = doctor.result?.status === 'ok' ? doctorRows(doctor.result.output) : []
  const hrneoOut = hrneo.result?.status === 'ok' ? doctorRows(hrneo.result.output) : []
  const pings = pingRows(tunnels)

  // Включение и выключение проверки связи -- переключатель, а не правка
  // конфига: обратное действие стоит на той же строке.
  const askPingToggle = (row) => {
    openSheet(
      confirmSheet({
        routerID,
        title: row.enabled ? `Выключить проверку связи у «${row.title}»?` : `Включить проверку связи у «${row.title}»?`,
        body: row.enabled
          ? 'Роутер перестанет пинговать этот туннель и перезапускать его сам. Тревога о падении по-прежнему придёт — по рукопожатию.'
          : 'Роутер начнёт пинговать туннель и перезапускать его сам, если ответа не будет.',
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
    <Overlay title="Настройки" backLabel="Назад" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">{routerName || 'Роутер'}</h1>
        <p class="router-lastseen">Как бот судит об этом роутере и что на нём стоит.</p>

        {error && <p class="state state-error">{error}</p>}

        {settings && (
          <Section title="Опрос и тревоги">
            <div class="card">
              {thresholdRows(settings).map((r) => (
                <DataRow key={r.key} title={r.title} code={r.code} value={r.value} />
              ))}
              <p class="card-foot">
                Эти числа живут в настройках бота, а не роутера: поменять их можно там, где он
                запущен. Здесь они показаны, чтобы было видно, через сколько придёт тревога.
              </p>
            </div>
          </Section>
        )}

        <Section title="Что стоит на роутере">
          <button type="button" class="btn btn-ghost btn-wide" disabled={audit.busy} onClick={() => audit.run('version_audit', {}, deadline)}>
            {audit.busy ? 'Спрашиваем роутер…' : 'Сверить версии'}
          </button>
          {audit.error && <p class="state state-error">{audit.error}</p>}
          {audit.result && audit.result.status !== 'ok' && (
            <p class="state state-error">Роутер не ответил: {audit.result.output || audit.result.status}</p>
          )}
          {auditOut.length > 0 && (
            <div class="card settings-card">
              {auditOut.map((r) => (
                <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueSub={r.sub} valueTone={r.tone} />
              ))}
            </div>
          )}
        </Section>

        <Section title="Проверка связи">
          {pings.length === 0 ? (
            <div class="card">
              <p class="traffic-detail">Роутер не сообщил ни одного туннеля.</p>
            </div>
          ) : (
            <div class="card">
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
              <p class="card-foot">
                Роутер сам пингует туннель и перезапускает его, если ответа нет. Задержка — это
                то, что он намерил последним замером.
              </p>
            </div>
          )}
          <button type="button" class="btn btn-ghost btn-wide" disabled={pingNow.busy} onClick={() => pingNow.run('pingcheck_now', {}, deadline).then((res) => { if (res?.status === 'ok') load() })}>
            {pingNow.busy ? 'Проверяем…' : 'Проверить связь сейчас'}
          </button>
          {pingNow.error && <p class="state state-error">{pingNow.error}</p>}
        </Section>

        <Section title="Проверить роутер изнутри">
          <div class="settings-actions">
            <button type="button" class="btn btn-ghost" disabled={doctor.busy} onClick={() => doctor.run('router_doctor', {}, deadline)}>
              {doctor.busy ? 'Смотрим…' : 'Осмотр роутера'}
            </button>
            <button type="button" class="btn btn-ghost" disabled={hrneo.busy} onClick={() => hrneo.run('hrneo_doctor', {}, deadline)}>
              {hrneo.busy ? 'Смотрим…' : 'Осмотр HR Neo'}
            </button>
          </div>
          {(doctor.error || hrneo.error) && <p class="state state-error">{doctor.error || hrneo.error}</p>}
          {[...doctorOut, ...hrneoOut].length > 0 && (
            <div class="card settings-card">
              {[...doctorOut, ...hrneoOut].map((r, i) => (
                <DataRow key={`${r.key}-${i}`} dot={r.tone} title={r.title} value={r.value} valueTone={r.tone} />
              ))}
            </div>
          )}
          {/* Доктор отвечает текстом; разобранные строки -- это его пересказ,
              и сам ответ обязан остаться доступным целиком. */}
          {(doctor.result?.status === 'ok' || hrneo.result?.status === 'ok') && (
            <>
              <button type="button" class="btn btn-ghost raw-toggle" onClick={() => setShowHelp((v) => !v)}>
                {showHelp ? 'Скрыть ответ целиком' : 'Ответ роутера целиком'}
              </button>
              {showHelp && (
                <pre class="raw-dump">{[doctor.result?.output, hrneo.result?.output].filter(Boolean).join('\n\n')}</pre>
              )}
            </>
          )}
        </Section>

        <Section title="Что умеет приложение">
          <div class="card">
            <p class="card-foot">
              <b>Роутер</b> — работает ли линия прямо сейчас и что с ней не так.{' '}
              <b>Туннели</b> — какая линия несёт трафик, кто подхватит и что через неё уходит.{' '}
              <b>Диагностика</b> — те же вопросы, заданные роутеру заново, и адрес, которым вас
              видно снаружи. <b>События</b> — что происходило за неделю.
            </p>
            <p class="card-foot">
              Уведомления остаются у бота: приложение не может разбудить того, кто его не открыл.
              Тревога придёт в чат, а из неё кнопка ведёт сюда.
            </p>
          </div>
        </Section>
      </div>
    </Overlay>
  )
}
