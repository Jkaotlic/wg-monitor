import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { parseRouteSnapshot } from '../routes.js'
import { tunnelsView } from '../tunnelsView.js'
import { humanAge } from '../labels.js'
import { Section } from '../ui/Section.jsx'
import { Hero } from '../ui/Hero.jsx'
import { StateTag } from '../ui/StateTag.jsx'
import { Stat } from '../ui/Stat.jsx'
import { Chain } from '../ui/Chain.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { NavCard } from '../ui/NavCard.jsx'

// Туннели: какая линия несёт трафик, кто подхватит, если она замолчит, и что
// не используется. Порядок блоков -- порядок вопросов оператора, а не порядок
// полей в снимке.
//
// Маршруты уехали отсюда на свой экран: сначала человек спрашивает "какая
// линия поднята", и только потом -- "что через неё идёт".
export function TunnelsTab({ routerID, asleep, onOpenRoutes }) {
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

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Туннели</h1>
        <button type="button" class="btn btn-ghost" disabled={busy} onClick={() => run('route_status', {}, deadline)}>
          {busy ? 'Читаю…' : 'Обновить'}
        </button>
      </div>

      {busy && snapshot == null && <p class="state">Роутер отвечает не мгновенно — читаем снимок…</p>}
      {error && <p class="state state-error">{error}</p>}
      {result && result.status !== 'ok' && (
        <p class="state state-error">Роутер не отдал снимок: {result.output || result.status}</p>
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

      {view.chain.length > 0 && (
        <Section title="Порядок подхвата">
          <div class="card" style="padding:0">
            <Chain
              links={view.chain.map((c) => ({
                ...c,
                title: c.role === 'active' ? 'Работает сейчас' : c.role === 'ready' ? 'Готов подхватить' : 'Выключен вручную',
                value: c.role === 'active' && c.handshakeAgeSec != null ? humanAge(c.handshakeAgeSec) : c.note,
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
              <DataRow
                key={t.id}
                title={t.name}
                code={t.id}
                value={t.live === 'up' ? 'поднят' : t.live === 'down' ? 'выключен' : 'неизвестно'}
                valueTone="muted"
              />
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
    </div>
  )
}
