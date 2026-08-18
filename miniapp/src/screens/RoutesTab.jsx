import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { parseRouteSnapshot, routingVerdict, policyRows, rulesByBind } from '../routes.js'
import { Section } from '../ui/Section.jsx'
import { Chip } from '../ui/Chip.jsx'

const KIND_LABEL = { dns: 'по имени сайта', static: 'по адресу сети' }
const POLICY_ROLE_LABEL = {
  active: 'активный',
  fallback: 'резерв',
  unavailable: 'недоступен',
}

function ruleTargets(rule) {
  const targets = rule.targets ?? []
  if (targets.length === 0) return rule.name || rule.id
  const shown = targets.slice(0, 3).map((t) => t.value).join(', ')
  return targets.length > 3 ? `${shown} и ещё ${targets.length - 3}` : shown
}

// Маршруты: только чтение. Управление появится, когда система научится
// верно читать привязку правил к политикам роутера (раздел 3 спеки Phase 5) --
// переключатель поверх неверной раскладки управлял бы выдумкой.
export function RoutesTab({ routerID, asleep }) {
  const { busy, result, error, run } = useCommand(routerID)
  const [snapshot, setSnapshot] = useState(null)

  useEffect(() => {
    setSnapshot(null)
    run('route_status', {}, { deadlineMs: asleep ? 6 * 60_000 : 90_000 })
    // Снимок запрашивается при каждой смене роутера; перезапрос -- кнопкой.
  }, [routerID])

  useEffect(() => {
    if (result?.status === 'ok') setSnapshot(parseRouteSnapshot(result.output))
  }, [result])

  const verdict = snapshot ? routingVerdict(snapshot) : null
  const policies = policyRows(snapshot)
  const groups = rulesByBind(snapshot)

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Маршруты</h1>
        <button type="button" class="btn btn-ghost" disabled={busy} onClick={() => run('route_status', {}, { deadlineMs: asleep ? 6 * 60_000 : 90_000 })}>
          {busy ? 'Читаю…' : 'Обновить'}
        </button>
      </div>
      <p class="router-lastseen">Решает, какой трафик идёт через VPN, а какой напрямую.</p>

      {busy && snapshot == null && <p class="state">Роутер отвечает не мгновенно — читаем снимок…</p>}
      {error && <p class="state state-error">{error}</p>}
      {result && result.status !== 'ok' && (
        <p class="state state-error">Роутер не отдал снимок маршрутизации: {result.output || result.status}</p>
      )}
      {result?.status === 'ok' && snapshot == null && (
        <p class="state state-error">Снимок пришёл, но разобрать его не удалось — покажем как есть ниже.</p>
      )}

      {verdict && (
        <Section title="Куда идёт трафик">
          <div class="card">
            <p class="traffic-title">{verdict.title}</p>
            <p class="traffic-detail">{verdict.detail}</p>
            {verdict.partial && (
              <p class="traffic-note">
                Снимок неполный: часть данных роутер не отдал ({snapshot.warnings.join('; ')}).
              </p>
            )}
          </div>
        </Section>
      )}

      {snapshot && (
        <Section title="Туннели в маршрутизации">
          {snapshot.tunnels?.length ? (
            <ul class="card list-reset">
              {snapshot.tunnels.map((t) => {
                const counts = snapshot.counts?.[t.id]
                const total = (counts?.dns ?? 0) + (counts?.static ?? 0)
                return (
                  <li key={t.id} class="row tunnel-row">
                    <span class="tunnel-name">
                      <span class="row-title">{t.name || t.id}</span>
                      {t.name && t.name !== t.id && <span class="tunnel-id">{t.id}</span>}
                    </span>
                    {t.default_route && <Chip tone="ok">основной маршрут</Chip>}
                    <span class="tunnel-sub">
                      {total > 0 ? `${total} правил ведут сюда` : 'правил на него нет'}
                      {counts?.hr_neo ? ` · из них ${counts.hr_neo} через HydraRoute` : ''}
                    </span>
                  </li>
                )
              })}
            </ul>
          ) : (
            <div class="card">
              <p class="traffic-detail">Роутер не сообщил ни одного туннеля.</p>
            </div>
          )}
        </Section>
      )}

      {policies.length > 0 && (
        <Section title="Политики">
          <ul class="card list-reset">
            {policies.map((p) => (
              <li key={p.name} class="row tunnel-row policy-row">
                <span class="row-title">
                  {p.name}
                  {p.egress ? <span class="policy-egress">{p.egress}</span> : null}
                </span>
                <span class="tunnel-sub">
                  {p.rules} правил
                  {p.hrNeo ? ` (${p.hrNeo} через HydraRoute)` : ''}
                </span>
                <ol class="policy-chain list-reset">
                  {p.chain.map((i, idx) => (
                    <li key={i.bind} class={`policy-link policy-link-${i.role}`}>
                      <span class="policy-link-order">{idx + 1}</span>
                      <span class="policy-link-name">{i.label}</span>
                      <span class="policy-link-role">{POLICY_ROLE_LABEL[i.role] ?? i.role}</span>
                    </li>
                  ))}
                </ol>
              </li>
            ))}
          </ul>
          <p class="admin-note">
            Первое доступное звено цепочки и несёт трафик политики, остальные ждут как резерв.
          </p>
        </Section>
      )}

      {groups.length > 0 && (
        <Section title="Правила">
          {groups.map((g) => (
            <div key={g.bind} class="rules-group">
              <h3 class="rules-bind">{g.bind}</h3>
              <ul class="card list-reset">
                {g.rules.slice(0, 20).map((r) => (
                  <li key={r.id} class="row tunnel-row">
                    <span class="row-title rules-target">{ruleTargets(r)}</span>
                    <span class="tunnel-sub">
                      {KIND_LABEL[r.kind] ?? r.kind}
                      {r.backend ? ` · ${r.backend}` : ''}
                      {r.enabled === false ? ' · выключено' : ''}
                    </span>
                  </li>
                ))}
              </ul>
              {g.rules.length > 20 && (
                <p class="admin-note">Показаны первые 20 из {g.rules.length}.</p>
              )}
            </div>
          ))}
        </Section>
      )}

      {snapshot && (
        <p class="admin-note">
          Менять маршруты отсюда пока нельзя: система ещё не читает привязку правил к политикам
          роутера верно, и переключатель управлял бы не тем, что показан выше.
        </p>
      )}
    </div>
  )
}
