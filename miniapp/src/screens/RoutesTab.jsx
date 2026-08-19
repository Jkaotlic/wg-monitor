import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import {
  parseRouteSnapshot,
  routingVerdict,
  defaultDestination,
  policyRows,
  tunnelRows,
  rulesByBind,
  defaultRouteBadge,
  tunnelRuleSummary,
  policyRuleSummary,
  ruleBackendLabel,
  visibleTunnelRows,
} from '../routes.js'
import { Section } from '../ui/Section.jsx'
import { Chip } from '../ui/Chip.jsx'
import { confirmSheet } from '../sheet.js'
import { deletePlanSummary } from '../routeAdd.js'

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

// Маршруты: только чтение. Привязка правил берётся из политик доступа
// роутера, поэтому раскладке ниже можно верить. Управление сюда не вынесено
// сознательно -- перенос правил между туннелями требует превью, подтверждения
// и отката, и он относится к фазе рабочего места оператора.
export function RoutesTab({ routerID, asleep, openSheet }) {
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

  // Удаление идёт в два шага: сначала агент считает план и говорит, что
  // именно исчезнет, и только потом человек подтверждает. Одношаговое
  // удаление правила, которого он не видит целиком, -- это удаление вслепую.
  const plan = useCommand(routerID)
  const [pendingRule, setPendingRule] = useState(null)

  useEffect(() => {
    if (!pendingRule || plan.result?.status !== 'ok') return
    const summary = deletePlanSummary(parseRouteSnapshot(plan.result.output))
    setPendingRule(null)
    openSheet?.(
      confirmSheet({
        routerID,
        title: `Убрать «${summary.title}»?`,
        body: summary.lines.length
          ? `Перестанет уходить в туннель: ${summary.lines.join(' · ')}`
          : 'Правило исчезнет с роутера.',
        action: 'route_delete',
        args: { kind: pendingRule.kind, route_id: pendingRule.id, preview_hash: summary.hash },
        buttonLabel: 'Убрать',
        danger: true,
        asleep,
        onDone: () => run('route_status', {}, { deadlineMs: asleep ? 6 * 60_000 : 90_000 }),
      }),
    )
  }, [plan.result])

  const askDelete = (rule) => {
    setPendingRule({ id: rule.id, kind: rule.kind })
    plan.run('route_delete_plan', { kind: rule.kind, route_id: rule.id }, { deadlineMs: asleep ? 6 * 60_000 : 90_000 })
  }

  const verdict = snapshot ? routingVerdict(snapshot) : null
  const policies = policyRows(snapshot)
  const tunnels = visibleTunnelRows(tunnelRows(snapshot))
  const groups = rulesByBind(snapshot)

  // Менять маршруты по неполной картине нельзя. Серая неактивная кнопка тут
  // не годится: она не объясняет, почему нельзя, -- поэтому кнопок просто
  // нет, а причина сказана словами ниже.
  const canMutate =
    Boolean(openSheet) &&
    snapshot != null &&
    !(snapshot.warnings?.length) &&
    !snapshot.singbox_router?.enabled

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
            {/* Вторая половина модели оператора: в туннель уходит только
                названное, а всё остальное -- сюда. Этой строки на экране не
                было вовсе, хотя без неё раскладка отвечает на половину
                вопроса. */}
            <p class="traffic-default">{defaultDestination(snapshot).text}</p>
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
          {tunnels.length ? (
            <ul class="card list-reset">
              {tunnels.map((t) => {
                const badge = defaultRouteBadge(t)
                return (
                  <li key={t.id} class="row tunnel-row">
                    <span class="tunnel-name">
                      <span class="row-title">{t.name}</span>
                      {t.name !== t.id && <span class="tunnel-id">{t.id}</span>}
                    </span>
                    {badge && <Chip tone={badge.tone}>{badge.text}</Chip>}
                    <span class="tunnel-sub">{tunnelRuleSummary(t)}</span>
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
              <li key={p.name} class="row tunnel-row">
                <span class="row-title">
                  {p.name}
                  {p.egress ? <span class="policy-egress">{p.egress}</span> : null}
                </span>
                <span class="tunnel-sub">{policyRuleSummary(p)}</span>
                {p.chain.length > 0 ? (
                  <ol class="policy-chain list-reset">
                    {p.chain.map((i, idx) => (
                      <li key={i.bind} class={`policy-link policy-link-${i.role}`}>
                        <span class="policy-link-order">{idx + 1}</span>
                        <span class="policy-link-name">{i.label}</span>
                        <span class="policy-link-role">{POLICY_ROLE_LABEL[i.role] ?? i.role}</span>
                      </li>
                    ))}
                  </ol>
                ) : (
                  // Пустая цепочка -- тоже правда, а не пробел: у политики
                  // нет ни одного интерфейса, и экран обязан сказать это,
                  // а не молчать пустым списком.
                  <p class="policy-chain-empty">привязки нет</p>
                )}
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
              <h3 class="rules-bind">{g.label}</h3>
              <ul class="card list-reset">
                {g.rules.slice(0, 20).map((r) => (
                  <li key={r.id} class="row tunnel-row">
                    <span class="row-title rules-target">{ruleTargets(r)}</span>
                    {canMutate && (
                      <button
                        type="button"
                        class="btn btn-ghost btn-row"
                        disabled={plan.busy}
                        onClick={() => askDelete(r)}
                      >
                        Убрать
                      </button>
                    )}
                    <span class="tunnel-sub">
                      {KIND_LABEL[r.kind] ?? r.kind}
                      {r.backend ? ` · ${ruleBackendLabel(r.backend)}` : ''}
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

      {snapshot && !canMutate && openSheet && (
        <p class="admin-note">
          {snapshot.singbox_router?.enabled
            ? 'Маршрут выбирает sing-box — правила ниже не действуют, и менять их отсюда бессмысленно.'
            : 'Снимок неполный — менять маршруты по нему нельзя.'}
        </p>
      )}

      {snapshot && (
        <p class="admin-note">
          Раскладка выше читается из политик доступа роутера — это то, куда трафик идёт на самом
          деле. Менять маршруты отсюда нельзя намеренно: перенос правил между туннелями требует
          превью и отката, и он появится вместе с рабочим местом оператора.
        </p>
      )}
    </div>
  )
}
