import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { parseRouteSnapshot, snapshotState } from '../routes.js'
import { confirmSheet } from '../sheet.js'
import { tunnelsView } from '../tunnelsView.js'
import { trafficSummary } from '../traffic.js'
import { humanAge } from '../labels.js'
import { Section } from '../ui/Section.jsx'
import { Hero } from '../ui/Hero.jsx'
import { StateTag } from '../ui/StateTag.jsx'
import { Stat } from '../ui/Stat.jsx'
import { Chain } from '../ui/Chain.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { NavCard } from '../ui/NavCard.jsx'
import { CabinetScreen } from './CabinetScreen.jsx'
import { ReplaceScreen } from './ReplaceScreen.jsx'

// Туннели: какая линия несёт трафик, кто подхватит, если она замолчит, и что
// не используется. Порядок блоков -- порядок вопросов оператора, а не порядок
// полей в снимке.
//
// Маршруты уехали отсюда на свой экран: сначала человек спрашивает "какая
// линия поднята", и только потом -- "что через неё идёт".
export function TunnelsTab({ routerID, asleep, onOpenRoutes, openSheet }) {
  const [cabinets, setCabinets] = useState(false)
  const [replacing, setReplacing] = useState(null)
  const { busy, result, error, run } = useCommand(routerID)
  const [snapshot, setSnapshot] = useState(null)

  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }

  useEffect(() => {
    setSnapshot(null)
    run('route_status', {}, deadline)
  }, [routerID])

  useEffect(() => {
    if (result?.status === 'ok') setSnapshot(parseRouteSnapshot(result.output))
  }, [result])

  const view = tunnelsView(snapshot)
  const phase = snapshotState({ busy, error, result, snapshot })
  // Обмен спрашивается отдельной командой и только по кнопке: ряд роутер
  // ведёт сам, но тянуть его при каждом открытии экрана незачем -- вопрос
  // «сколько прошло за сутки» задают редко и осознанно.
  const traffic = useCommand(routerID)
  const trafficOut = traffic.result?.status === 'ok' ? trafficSummary(traffic.result.output) : null

  // Включение и выключение идёт по идентификатору туннеля (tunnel_power,
  // awg-manager control/start|stop). Прежняя пара ndmc-действий умела только
  // NDMS-интерфейсы, и у opkg-туннеля кнопки не было вовсе -- хотя половина
  // туннелей живого роутера именно такие.
  //
  // Активную линию отсюда не выключают: она несёт трафик прямо сейчас, и
  // «выключить» на ней -- не переключатель, а обрыв. Для неё на главном
  // экране есть перезапуск.
  const toggleButton = (t) => {
    if (!openSheet || t.live === 'unknown') return null
    const up = t.live === 'up'
    return (
      <button
        type="button"
        class="btn btn-ghost btn-row"
        onClick={() =>
          openSheet(
            confirmSheet({
              routerID,
              title: up ? `Выключить «${t.name}»?` : `Включить «${t.name}»?`,
              body: up
                ? `Роутер опустит интерфейс. Трафик, который шёл через «${t.name}», пойдёт по следующему звену цепочки или напрямую. Включить обратно — этой же кнопкой.`
                : `Роутер поднимет интерфейс. Если он стоит в цепочке выше работающего, трафик перейдёт на него.`,
              action: 'tunnel_power',
              args: { tunnel_id: t.tunnelID ?? t.id, on: !up },
              buttonLabel: up ? 'Выключить' : 'Включить',
              danger: up,
              asleep,
              onDone: () => run('route_status', {}, deadline),
            }),
          )
        }
      >
        {up ? 'Выключить' : 'Включить'}
      </button>
    )
  }

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Туннели</h1>
        <button type="button" class="btn btn-ghost" disabled={busy} onClick={() => run('route_status', {}, deadline)}>
          {busy ? 'Читаю…' : 'Обновить'}
        </button>
      </div>

      {phase === 'loading' && <p class="state">Роутер отвечает не мгновенно — читаем снимок…</p>}
      {phase === 'error' && <p class="state state-error">{error}</p>}
      {phase === 'refused' && (
        <p class="state state-error">Роутер не отдал снимок: {result.output || result.status}</p>
      )}
      {/* Ответ пришёл, а снимка в нём нет. Молчать здесь нельзя: пустой экран
          неотличим от «туннелей нет», и человек будет искать поломку в
          роутере, а не в том, что приложение не поняло ответ. */}
      {phase === 'unreadable' && (
        <p class="state state-error">
          Роутер ответил, но снимок не разобрать. Так отвечает старый агент — обновите его на этом роутере.
        </p>
      )}

      {view.active && (
        <Section title="Линия, которая работает">
          <Hero>
            {/* Возраст рукопожатия живёт в плитке ниже. Повторять его здесь
                значило бы назвать одно показание дважды и в разных единицах. */}
            <StateTag>туннель поднят</StateTag>
            <h2 class="traffic-title" style="margin-top:8px">{view.active.name}</h2>
            {view.active.iface && <p class="data-row-code">интерфейс {view.active.iface}</p>}
            <div class="stat-grid" style="margin:14px 0 16px">
              <Stat
                label="рукопожатие"
                value={view.active.handshakeAgeSec != null ? humanAge(view.active.handshakeAgeSec) : null}
                note={view.active.handshakeAgeSec != null ? 'назад, канал живой' : 'роутер не сообщил'}
              />
              <Stat label="несёт" value={view.active.rules} unit="назн." note={`политика ${view.policyName}`} />
            </div>
          </Hero>
        </Section>
      )}

      {snapshot && !view.active && (
        <Section title="Линия, которая работает">
          <Hero cold>
            <StateTag tone="danger">ни один туннель не несёт трафик</StateTag>
            <p class="traffic-detail" style="padding-bottom:16px">
              Трафик уходит через провайдера. Если так не задумано — поднимите линию на экране ниже.
            </p>
          </Hero>
        </Section>
      )}

      {view.active && (
        <Section title="Обмен за сутки">
          <div class="card">
            <div class="stat-grid" style="padding:14px">
              <Stat label="принято" value={trafficOut?.known ? trafficOut.rx : null} note={trafficOut?.empty ? 'за сутки ничего' : 'роутер посчитал сам'} />
              <Stat label="отдано" value={trafficOut?.known ? trafficOut.tx : null} note={trafficOut?.known ? `точек в ряду: ${trafficOut.points}` : 'нажмите «Показать обмен»'} />
            </div>
            {traffic.result && traffic.result.status !== 'ok' && (
              <p class="card-foot card-foot-bad">
                Роутер не отдал ряд: {traffic.result.output || traffic.result.status}
              </p>
            )}
          </div>
          <button
            type="button"
            class="btn btn-ghost btn-wide"
            disabled={traffic.busy}
            onClick={() => traffic.run('tunnel_traffic', { tunnel_id: view.active.id, period: '24h' }, deadline)}
          >
            {traffic.busy ? 'Считаем…' : 'Показать обмен'}
          </button>
          {traffic.error && <p class="state state-error">{traffic.error}</p>}
        </Section>
      )}

      {view.chain.length > 0 && (
        <Section title="Порядок подхвата">
          <div class="card" style="padding:0">
            <Chain
              links={view.chain.map((c) => ({
                ...c,
                title: c.role === 'active' ? 'Работает сейчас' : c.role === 'ready' ? 'Готов подхватить' : 'Выключен вручную',
                value: c.role === 'active' && c.handshakeAgeSec != null ? humanAge(c.handshakeAgeSec) : c.note,
                action: c.role === 'active' ? null : toggleButton(c),
              }))}
            />
            <p class="card-foot">
              Линия поднимается одна за раз: замолчит верхняя — роутер возьмёт следующую.
            </p>
          </div>
        </Section>
      )}

      {view.unused.length > 0 && (
        <Section title={`Не используются · ${view.unused.length}`}>
          <div class="card" style="padding:0">
            {view.unused.map((t) => (
              <div key={t.id} class="settings-row">
                <DataRow
                  title={t.name}
                  code={t.id}
                  value={t.live === 'up' ? 'поднят' : t.live === 'down' ? 'выключен' : 'неизвестно'}
                  valueTone="muted"
                />
                {toggleButton(t)}
              </div>
            ))}
          </div>
        </Section>
      )}

      {view.active && (
        <div style="margin-top:24px">
          <NavCard
            title="Маршруты"
            note={`${view.active.rules} назн.`}
            onClick={onOpenRoutes}
          />
        </div>
      )}

      {snapshot && (
        <div style="margin-top:12px">
          <NavCard
            title="Новая линия из кабинета"
            note="Amnezia · HideMy"
            onClick={() => setCabinets(true)}
          />
        </div>
      )}

      {/* Замена конфига предлагается для работающей линии: смысл операции --
          заменить то, чем сейчас ходит трафик, не потеряв прежний туннель. */}
      {view.active && view.policyName && (
        <div style="margin-top:12px">
          <NavCard
            title="Заменить конфиг линии"
            note={view.active.name}
            onClick={() => setReplacing(view.active)}
          />
        </div>
      )}

      {replacing && (
        <ReplaceScreen
          routerID={routerID}
          tunnel={replacing}
          policyName={view.policyName}
          onClose={() => setReplacing(null)}
          onDone={() => run('route_status', {}, deadline)}
        />
      )}

      {cabinets && (
        <CabinetScreen
          routerID={routerID}
          asleep={asleep}
          onClose={() => setCabinets(false)}
          onIssued={() => run('route_status', {}, deadline)}
        />
      )}
    </div>
  )
}
