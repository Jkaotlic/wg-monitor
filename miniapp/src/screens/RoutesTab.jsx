import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import {
  parseRouteSnapshot,
  snapshotState,
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
  rebindTargets,
  promoteTargets,
  canRebindTunnel,
} from '../routes.js'
import { rulesCount, tunnelLiveLabel } from '../labels.js'
import { Section } from '../ui/Section.jsx'
import { Chip } from '../ui/Chip.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { Overlay } from '../ui/Overlay.jsx'
import { confirmSheet } from '../sheet.js'
import { deletePlanSummary } from '../routeAdd.js'
import { RouteAddScreen } from './RouteAddScreen.jsx'

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

// Маршруты: раскладка и управление. Привязка правил берётся из политик
// доступа роутера, поэтому тому, что показано, можно верить, а тому, что
// меняется, -- тем более: каждая правка идёт через превью от агента и
// подтверждение в шите, и каждая обратима другой кнопкой этого же экрана.
export function RoutesTab({ routerID, asleep, openSheet }) {
  const { busy, result, error, run } = useCommand(routerID)
  const [snapshot, setSnapshot] = useState(null)
  // Спящий роутер отвечает минутами, и дедлайн у всех команд экрана один:
  // разные сроки на соседних кнопках -- это разное поведение без причины.
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const refresh = () => run('route_status', {}, deadline)

  useEffect(() => {
    setSnapshot(null)
    run('route_status', {}, deadline)
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
        onDone: refresh,
      }),
    )
  }, [plan.result])

  const askDelete = (rule) => {
    setPendingRule({ id: rule.id, kind: rule.kind })
    plan.run('route_delete_plan', { kind: rule.kind, route_id: rule.id }, deadline)
  }

  // Перенос и смена главного начинаются с выбора цели. Цель выбирается
  // отдельным слоем, а не выпадающим списком: список туннелей -- это строки
  // с состоянием, и в 44 px выпадающего меню состояние не поместится.
  const [picker, setPicker] = useState(null)
  const [adding, setAdding] = useState(false)

  const phase = snapshotState({ busy, error, result, snapshot })

  const verdict = snapshot ? routingVerdict(snapshot) : null
  const policies = policyRows(snapshot)
  const rows = tunnelRows(snapshot)
  const tunnels = visibleTunnelRows(rows)
  const groups = rulesByBind(snapshot)

  // Перенос забирает ВСЁ, что ведёт в туннель, -- и правила, и политику,
  // которую он несёт (RouteRebind, route_rebind.go). Поэтому и спрашивается
  // он на строке туннеля, а не на группе правил: группа -- это привязка,
  // а переносится линия целиком.
  const askRebind = (src) => {
    const options = rebindTargets(rows, src.id)
    if (options.length === 0) return
    setPicker({
      title: 'Куда перенести правила',
      subtitle: `Всё, что сейчас ведёт в «${src.name}» (${rulesCount(src.total)})`,
      options: options.map((dst) => ({
        id: dst.id,
        title: dst.name,
        sub: `${tunnelLiveLabel(dst.live)} · ${tunnelRuleSummary(dst)}`,
        pick: () =>
          confirmSheet({
            routerID,
            title: `Перенести всё в «${dst.name}»?`,
            body: `${rulesCount(src.total)} уедут из «${src.name}» в «${dst.name}». В «${src.name}» не останется ничего — вернуть можно этой же кнопкой на «${dst.name}».`,
            action: 'route_rebind',
            args: { src_tunnel_id: src.id, dst_tunnel_id: dst.id },
            buttonLabel: 'Перенести',
            asleep,
            onDone: refresh,
          }),
      })),
    })
  }

  // Главным делается звено, уже стоящее в цепочке политики. Выключенный
  // туннель повысить можно, и порядок сменится, но трафик пойдёт через него
  // не раньше, чем он поднимется -- об этом экран говорит до нажатия.
  const askPromote = (src) => {
    const targets = promoteTargets(snapshot, src.id)
    if (targets.length === 0) return
    const ruleText = (policyName) => {
      const p = policies.find((row) => row.name === policyName)
      return p ? ` (${policyRuleSummary(p)})` : ''
    }
    setPicker({
      title: 'Кто станет главным',
      subtitle: `Сейчас правила политики идут через «${src.name}»`,
      options: targets.map((t) => ({
        id: `${t.policyName}:${t.tunnelID}`,
        title: t.tunnelName,
        sub: `политика «${t.policyName}» · ${tunnelLiveLabel(t.live)}`,
        pick: () =>
          confirmSheet({
            routerID,
            title: `Сделать «${t.tunnelName}» главным?`,
            body:
              `Правила политики «${t.policyName}»${ruleText(t.policyName)} пойдут через «${t.tunnelName}» вместо «${src.name}». Вернуть обратно можно этой же кнопкой.` +
              (t.live === 'down'
                ? ` «${t.tunnelName}» сейчас выключен: порядок сменится сразу, а трафик пойдёт через него не раньше, чем он поднимется.`
                : ''),
            action: 'route_policy_promote',
            args: { policy_name: t.policyName, tunnel_id: t.tunnelID },
            buttonLabel: 'Сделать главным',
            asleep,
            onDone: refresh,
          }),
      })),
    })
  }

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
        <button type="button" class="btn btn-ghost" disabled={busy} onClick={refresh}>
          {busy ? 'Читаю…' : 'Обновить'}
        </button>
      </div>
      <p class="router-lastseen">Решает, какой трафик идёт через VPN, а какой напрямую.</p>

      {phase === 'loading' && <p class="state">Роутер отвечает не мгновенно — читаем снимок…</p>}
      {phase === 'error' && <p class="state state-error">{error}</p>}
      {phase === 'refused' && (
        <p class="state state-error">Роутер не отдал снимок маршрутизации: {result.output || result.status}</p>
      )}
      {phase === 'unreadable' && (
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

      {/* Сигнальный цвет -- одному действию на экране, и это оно: всё
          остальное здесь либо читается, либо правит уже существующее. */}
      {canMutate && (
        <button type="button" class="btn btn-primary btn-wide" onClick={() => setAdding(true)}>
          Отправить в туннель
        </button>
      )}

      {snapshot && (
        <Section title="Туннели в маршрутизации">
          {tunnels.length ? (
            <ul class="card list-reset">
              {tunnels.map((t) => {
                const badge = defaultRouteBadge(t)
                const canRebind = canMutate && canRebindTunnel(t) && rebindTargets(rows, t.id).length > 0
                const canPromote = canMutate && promoteTargets(snapshot, t.id).length > 0
                return (
                  <li key={t.id} class="row tunnel-row">
                    <span class="tunnel-name">
                      <span class="row-title">{t.name}</span>
                      {t.name !== t.id && <span class="tunnel-id">{t.id}</span>}
                    </span>
                    {badge && <Chip tone={badge.tone}>{badge.text}</Chip>}
                    <span class="tunnel-sub">{tunnelRuleSummary(t)}</span>
                    {(canRebind || canPromote) && (
                      <span class="row-actions">
                        {canRebind && (
                          <button type="button" class="btn btn-ghost btn-row" onClick={() => askRebind(t)}>
                            Перенести всё
                          </button>
                        )}
                        {canPromote && (
                          <button type="button" class="btn btn-ghost btn-row" onClick={() => askPromote(t)}>
                            Сделать главным
                          </button>
                        )}
                      </span>
                    )}
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
          деле. Каждую правку роутер сначала считает планом и показывает его целиком, и только
          потом применяет; обратная кнопка есть у каждой.
        </p>
      )}

      {/* Выбор цели -- свой слой поверх экрана. Подтверждение он не заменяет:
          выбранное едет в тот же шит, что и всё остальное. */}
      {picker && (
        <Overlay title={picker.title} backLabel="Маршруты" onBack={() => setPicker(null)}>
          <div class="screen">
            <p class="router-lastseen">{picker.subtitle}</p>
            <ul class="card list-reset">
              {picker.options.map((o) => (
                <ListRow
                  key={o.id}
                  title={o.title}
                  sub={o.sub}
                  onClick={() => {
                    setPicker(null)
                    openSheet(o.pick())
                  }}
                />
              ))}
            </ul>
          </div>
        </Overlay>
      )}

      {adding && (
        <RouteAddScreen
          routerID={routerID}
          asleep={asleep}
          snapshot={snapshot}
          openSheet={openSheet}
          onClose={() => setAdding(false)}
          onApplied={refresh}
        />
      )}
    </div>
  )
}
