import { agentReplyText } from '../errorText.js'
import { useEffect, useRef, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { fetchRouterSettings, fetchRouterChecks, fetchAwg3Issuable } from '../api.js'
import { parseRouteSnapshot, snapshotState, tunnelRuleSummary, withCheckVerdict } from '../routes.js'
import { confirmSheet, localSheet } from '../sheet.js'
import { tunnelsView } from '../tunnelsView.js'
import { tunnelList, TUNNEL_TEXTS } from '../tunnelDelete.js'
import { cabinetPerms } from '../cabinetKeys.js'
import { CONFIG_SOURCES_TITLE, configSourceChoices, configSourceTarget } from '../configSources.js'
import { trafficSummary, trafficView } from '../traffic.js'
import { humanAge } from '../labels.js'
import { Section } from '../ui/Section.jsx'
import { Hero } from '../ui/Hero.jsx'
import { StateTag } from '../ui/StateTag.jsx'
import { Stat } from '../ui/Stat.jsx'
import { Chain } from '../ui/Chain.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { useOnClose } from '../useOnClose.js'
import { ListRow } from '../ui/ListRow.jsx'
import { ReplaceScreen } from './ReplaceScreen.jsx'
import { TunnelScreen } from './TunnelScreen.jsx'
import { ConfImportScreen } from './ConfImportScreen.jsx'
import { ErrorLine } from '../ui/ErrorLine.jsx'

// VPN-туннели: какой из них несёт трафик, кто подхватит, если он замолчит, и что
// не используется. Порядок блоков -- порядок вопросов оператора, а не порядок
// полей в снимке.
//
// Маршруты уехали отсюда на свой экран: сначала человек спрашивает "какой
// VPN-туннель поднят", и только потом -- "что через него идёт".
// Заголовок строки в цепочке. «Выключен вручную» на упавшем звене был
// докладом о чужом решении там, где случилась поломка -- а это два разных
// вывода и два разных действия.
const CHAIN_TITLE = {
  active: 'Работает сейчас',
  activeDown: 'Несёт трафик, но не отвечает',
  ready: 'Готов подхватить',
  down: 'Не отвечает',
  off: 'Выключен вручную',
  unknown: 'Состояние неизвестно',
  activeUnknown: 'Назначен несущим, проверка неизвестна',
  checkUnknown: 'Проверка неизвестна',
}

const SOURCES_WAIT_MS = 8000

// Кабинет -- слой навигации (cabinet), а не внутреннее состояние вкладки:
// его адрес переживает обновление страницы, и «назад» Telegram закрывает его.
// onOpenRebind(tunnelID) -- «Маршруты» с выбором цели переноса для этого
// VPN-туннеля. routesOpen -- слой «Маршрутов» открыт поверх вкладки: после его
// закрытия снимок перечитывается, правила могли уехать.
export function TunnelsTab({ routerID, asleep, onOpenRoutes, onOpenRebind, openSheet, isAdmin = false, layer = null, layerParams = {}, openLayer, closeLayer, cabinetOpen = false, routesOpen = false }) {
  // Роль решает, рисовать ли удаление и загрузку конфига; не узнали -- кнопок
  // нет, граница всё равно на сервере.
  // roleStatus: loading | ok | error -- сбой чтения роли не должен молча
  // убирать «Загрузить .conf» из листа (финальное ревью v0.52, п. 4).
  const [role, setRole] = useState('')
  const [roleStatus, setRoleStatus] = useState('loading')
  const { busy, result, error, run } = useCommand(routerID)
  const [snapshot, setSnapshot] = useState(null)
  // Вердикт проверок tunnel_* -- чтобы туннель с поднятым интерфейсом и мёртвой
  // удалённой стороной не звался «работает» (см. withCheckVerdict).
  const [checks, setChecks] = useState(null)

  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }

  // «Новый VPN-туннель» (v0.52): лист «Откуда взять конфиг». Панели VPN-сервера
  // грузятся заранее; сбой списка -- пункт с повтором, а не тишина.
  const [awg3, setAwg3] = useState({ status: 'loading', panels: [] })
  // Ответ чтения, пришедший после смены роутера, ничего не пишет: иначе роль
  // и панели прежнего роутера переехали бы на новый (финальное ревью, п. 6).
  const routerRef = useRef(routerID)
  routerRef.current = routerID
  const loads = useRef({ awg3: null, role: null })
  const loadAwg3 = () => {
    const rid = routerID
    const p = fetchAwg3Issuable(rid)
      .then((r) => ({ status: 'ok', panels: r?.panels ?? [] }))
      .catch(() => ({ status: 'error', panels: [] }))
      .then((next) => {
        if (routerRef.current === rid) setAwg3(next)
        return next
      })
    loads.current.awg3 = p
    return p
  }
  useEffect(() => {
    setAwg3({ status: 'loading', panels: [] })
    loadAwg3()
  }, [routerID])

  const loadRole = () => {
    const rid = routerID
    const p = fetchRouterSettings(rid)
      .then((st) => ({ status: 'ok', role: st?.role ?? '' }))
      .catch(() => ({ status: 'error', role: '' }))
      .then((next) => {
        if (routerRef.current === rid) {
          setRole(next.role)
          setRoleStatus(next.status)
        }
        return next
      })
    loads.current.role = p
    return p
  }

  // Лист закрывается после onDone, поэтому повторное открытие -- следующим
  // тиком, с уже перечитанным. Загрузка .conf -- всем, кто управляет
  // туннелями (админ, владелец, оператор: решение 01.10). Последнее известное
  // читается через ref: onDone приходит из замыкания старого рендера.
  const latest = useRef({})
  latest.current = { awg3, role, roleStatus }
  const askSource = (over = {}) => {
    const cur = { ...latest.current, ...over }
    const rid = routerID
    openSheet(
      localSheet({
        title: CONFIG_SOURCES_TITLE,
        body: 'Конфиг встанет на роутер новым VPN-туннелем рядом с остальными.',
        choices: configSourceChoices({ isAdmin, canImport: cabinetPerms(cur.role).manage, roleStatus: cur.roleStatus, awg3: cur.awg3 }),
        perform: (_typed, _values, value) => {
          if (value === 'awg3-retry') return loadAwg3().then((next) => ({ retried: true, rid, over: { awg3: next } }))
          if (value === 'conf-retry') return loadRole().then((next) => ({ retried: true, rid, over: { role: next.role, roleStatus: next.status } }))
          const target = configSourceTarget(value)
          if (target) openLayer?.(target.overlay, target.params)
          return null
        },
        onDone: (resp) => {
          if (resp?.retried && routerRef.current === resp.rid) setTimeout(() => askSource(resp.over), 0)
        },
      }),
    )
  }

  // Лист считает варианты при открытии и сам уже не обновится, поэтому кнопка
  // дожидается чтения панелей и роли (занятая, пока ждёт) и открывает лист с
  // итоговыми вариантами. Зависшее чтение -- через SOURCES_WAIT_MS как сбой:
  // пункт с повтором, а не вечная «загрузка» (финальное ревью, п. 5).
  const [opening, setOpening] = useState(false)
  const openSources = async () => {
    if (opening) return
    const cur = latest.current
    if (cur.awg3.status !== 'loading' && cur.roleStatus !== 'loading') return askSource()
    const rid = routerID
    setOpening(true)
    let timer
    const gave = await Promise.race([
      Promise.all([loads.current.awg3, loads.current.role]),
      new Promise((resolve) => {
        timer = setTimeout(() => resolve(null), SOURCES_WAIT_MS)
      }),
    ])
    clearTimeout(timer)
    setOpening(false)
    if (routerRef.current !== rid) return
    const now = latest.current
    if (gave) return askSource({ awg3: gave[0], role: gave[1].role, roleStatus: gave[1].status })
    askSource({
      awg3: now.awg3.status === 'loading' ? { status: 'error', panels: [] } : now.awg3,
      roleStatus: now.roleStatus === 'loading' ? 'error' : now.roleStatus,
    })
  }

  // Кабинет закрыт -- в нём мог появиться новый VPN-туннель: переспросить.
  useOnClose(cabinetOpen, () => run('route_status', {}, deadline))
  useOnClose(routesOpen, () => run('route_status', {}, deadline))

  useEffect(() => {
    setSnapshot(null)
    setRole('')
    setRoleStatus('loading')
    run('route_status', {}, deadline)
    loadRole()
  }, [routerID])

  useEffect(() => {
    if (result?.status === 'ok') setSnapshot(parseRouteSnapshot(result.output))
  }, [result])

  // Проверки перечитываются вместе со снимком: оба -- про одно и то же «сейчас».
  // Сбой загрузки не глотается (MINI-06): без вердикта проверок поднятый
  // интерфейс -- «состояние неизвестно», а не «работает».
  const [checksFailed, setChecksFailed] = useState(false)
  useEffect(() => {
    let alive = true
    fetchRouterChecks(routerID)
      .then((ev) => {
        if (!alive) return
        setChecks(ev)
        setChecksFailed(false)
      })
      .catch(() => {
        if (alive) setChecksFailed(true)
      })
    return () => {
      alive = false
    }
  }, [routerID, result])

  const shown = withCheckVerdict(snapshot, checks, { failed: checksFailed })
  const view = tunnelsView(shown)
  const list = tunnelList(shown)
  const phase = snapshotState({ busy, error, result, snapshot })
  // Обмен подтягивается сам, как только известен активный VPN-туннель. Раньше он
  // ждал кнопки, и карточка держала «неизвестно» -- то есть экран просил у
  // человека работу, которую мог сделать сам. Ряд роутер ведёт всё равно;
  // кнопка осталась способом пересчитать принудительно.
  const traffic = useCommand(routerID)
  const trafficOut = traffic.result?.status === 'ok' ? trafficSummary(traffic.result.output) : null

  // Заменить можно только то, что сервер найдёт: старт замены сверяет
  // old_tunnel_id с событиями tunnel_<id> этого роутера
  // (miniappResolveTunnelArgs), а агент пишет их лишь для типов awg/wg
  // (checks/tunnels.go). Доказательство -- то же событие в checks.tunnels:
  // нет его -- кнопка вела бы в гарантированный отказ unknown_tunnel.
  const resolvable = Boolean(view.active && (checks?.tunnels ?? []).some((c) => c.tunnel_id === view.active.id))
  const replaceFromHero = Boolean(openLayer && resolvable && view.policyName && !list.some((t) => t.id === view.active.id))

  const activeTunnelID = view.active?.id
  useEffect(() => {
    if (activeTunnelID) traffic.run('tunnel_traffic', { tunnel_id: activeTunnelID, period: '24h' }, deadline)
  }, [routerID, activeTunnelID])

  // Включение и выключение идёт по идентификатору туннеля (tunnel_power,
  // awg-manager control/start|stop). Прежняя пара ndmc-действий умела только
  // NDMS-интерфейсы, и у opkg-туннеля кнопки не было вовсе -- хотя половина
  // туннелей живого роутера именно такие.
  //
  // Активный VPN-туннель отсюда не выключают: он несёт трафик прямо сейчас, и
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
              title: up ? `Выключить «${t.title ?? t.name}»?` : `Включить «${t.title ?? t.name}»?`,
              body: up
                ? `Роутер опустит интерфейс. Трафик, который шёл через «${t.title ?? t.name}», пойдёт по следующему звену цепочки или напрямую. Включить обратно — этой же кнопкой.`
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

  // Упавшему звену кнопка «Включить» не помогает: оно и так включено, роутер
  // просто не смог его поднять. Единственное осмысленное действие здесь --
  // перезапуск, и предлагать надо именно его.
  const restartButton = (t) => {
    if (!openSheet) return null
    return (
      <button
        type="button"
        class="btn btn-ghost btn-row"
        onClick={() =>
          openSheet(
            confirmSheet({
              routerID,
              title: `Перезапустить «${t.title ?? t.name}»?`,
              body: `VPN-туннель включён, но не поднялся. Роутер опустит и снова поднимет интерфейс — если дело в зависшем соединении, это его чинит. Трафик по цепочке идёт мимо него и сейчас.`,
              action: 'tunnel_restart',
              args: { tunnel_id: t.tunnelID ?? t.id },
              buttonLabel: 'Перезапустить',
              asleep,
              onDone: () => run('route_status', {}, deadline),
            }),
          )
        }
      >
        Перезапустить
      </button>
    )
  }

  // Что предложить звену цепочки. Активное трогать нечем -- оно несёт трафик
  // прямо сейчас, и «выключить» на нём не переключатель, а обрыв.
  const chainAction = (c) => {
    if (['active', 'unknown', 'activeUnknown', 'checkUnknown'].includes(c.role)) return null
    if (c.role === 'down' || c.role === 'activeDown') return restartButton(c)
    return toggleButton(c)
  }

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">VPN-туннели</h1>
        <button type="button" class="btn btn-ghost" disabled={busy} onClick={() => run('route_status', {}, deadline)}>
          {busy ? 'Читаю…' : 'Обновить'}
        </button>
      </div>

      {/* Кнопка не ждёт снимка: «Откуда взять конфиг» от него не зависит. Без
          снимка экран загрузки .conf не сверяет имя с уже занятыми
          (tunnelNameProblem пропускает проверку) -- дубль отклонит сервер. */}
      {openLayer && openSheet && (
        <button type="button" class="btn btn-primary btn-wide new-tunnel" disabled={opening} onClick={openSources}>
          {opening ? 'Читаю…' : 'Новый VPN-туннель'}
        </button>
      )}

      {phase === 'loading' && <p class="state">Роутер отвечает не мгновенно — читаем снимок…</p>}
      {phase === 'error' && <ErrorLine text={error} busy={busy} onRetry={() => run('route_status', {}, deadline)} />}
      {phase === 'refused' && (
        <p class="state state-error">{agentReplyText(result, 'Роутер не отдал снимок — попробуйте ещё раз через минуту.')}</p>
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
        <Section title={view.active.live === 'down' || view.active.checkUnknown ? 'VPN-туннель, который несёт трафик' : 'VPN-туннель, который работает'}>
          <Hero>
            {/* Возраст рукопожатия живёт в плитке ниже. Повторять его здесь
                значило бы назвать одно показание дважды и в разных единицах. */}
            {view.active.live === 'down' ? (
              <StateTag tone="danger">VPN-туннель не отвечает</StateTag>
            ) : view.active.checkUnknown ? (
              <StateTag tone="warn">поднят, проверка не пришла: сервер не ответил</StateTag>
            ) : view.active.unverified ? (
              <StateTag tone="warn">поднят, не проверено</StateTag>
            ) : (
              <StateTag>VPN-туннель поднят</StateTag>
            )}
            <h2 class="traffic-title" style="margin-top:8px">{view.active.title}</h2>
            {/* Идентификатор и интерфейс -- инженерия: они стоят подписью под
                именем, а не вместо него. */}
            <p class="data-row-code">
              {view.active.code}
              {view.active.iface ? ` · интерфейс ${view.active.iface}` : ''}
            </p>
            <div class="stat-grid" style="margin:14px 0 16px">
              <Stat
                label="обмен ключами"
                value={view.active.handshakeAgeSec != null ? humanAge(view.active.handshakeAgeSec) : null}
                note={
                  view.active.handshakeAgeSec == null
                    ? 'роутер не сообщил'
                    : view.active.live === 'down'
                      ? 'назад, но трафик не проходит'
                      : view.active.checkUnknown
                        ? 'назад, проверка не пришла'
                      : 'назад, канал живой'
                }
              />
              <Stat label="несёт" value={view.active.rules} unit="назн." note={view.active.rulesNote || undefined} />
            </div>
            {/* Несущий VPN-туннель не managed-типа не попадает в «Все VPN-туннели»,
                а значит и на свой экран: замена конфига -- здесь (финал п. 1). */}
            {replaceFromHero && (
              <button type="button" class="btn btn-ghost btn-wide hero-replace" onClick={() => openLayer('replace', { tunnel: view.active, policyName: view.policyName })}>
                Заменить конфиг
              </button>
            )}
          </Hero>
        </Section>
      )}

      {snapshot && !view.active && (
        <Section title="VPN-туннель, который работает">
          <Hero cold>
            <StateTag tone="danger">ни один VPN-туннель не несёт трафик</StateTag>
            <p class="traffic-detail" style="padding-bottom:16px">
              Трафик уходит через провайдера. Если так не задумано — поднимите VPN-туннель на экране ниже.
            </p>
          </Hero>
        </Section>
      )}

      {view.active && (() => {
        const failText = traffic.error || (traffic.result && traffic.result.status !== 'ok' ? 'Роутер не отдал обмен за сутки — попробуйте ещё раз.' : '')
        const tv = trafficView(trafficOut, { busy: traffic.busy, error: failText })
        const again = () => traffic.run('tunnel_traffic', { tunnel_id: view.active.id, period: '24h' }, deadline)
        return (
          <Section title="Обмен за сутки">
            <div class={tv.invite ? 'card traffic-invite' : 'card'}>
              {tv.invite ? (
                <p class={tv.error ? 'state state-error traffic-detail' : 'traffic-detail'}>{tv.text}</p>
              ) : (
                <div class="stat-grid" style="padding:14px">
                  <Stat label="принято" value={tv.rx} note={tv.note} />
                  <Stat label="отдано" value={tv.tx} />
                </div>
              )}
              <button type="button" class="btn btn-ghost btn-wide" disabled={traffic.busy} onClick={again}>
                {tv.button}
              </button>
            </div>
          </Section>
        )
      })()}

      {view.chain.length > 0 && (
        <Section title="Порядок подхвата">
          <div class="card card-rows">
            <Chain
              links={view.chain.map((c) => ({
                ...c,
                // Заголовок звена -- его роль; имя VPN-туннеля идёт подписью, и
                // подписью человеческой: раньше у безымянного VPN-туннеля здесь
                // стояло имя интерфейса вида OpkgTun11.
                name: c.title,
                title: CHAIN_TITLE[c.role] ?? 'Состояние неизвестно',
                value: c.role === 'active' && c.handshakeAgeSec != null ? humanAge(c.handshakeAgeSec) : c.note,
                action: chainAction(c),
              }))}
            />
            <p class="card-foot">
              Трафик несёт один VPN-туннель за раз: замолчит верхний — роутер возьмёт следующий.
            </p>
          </div>
        </Section>
      )}

      {view.unused.length > 0 && (
        <Section title={`Не используются · ${view.unused.length}`}>
          <div class="card card-rows">
            {view.unused.map((t) => (
              <div key={t.id} class="settings-row">
                <DataRow
                  title={t.title}
                  code={t.code}
                  value={t.live === 'up' ? 'поднят' : t.live === 'down' ? 'выключен' : 'неизвестно'}
                  valueTone="muted"
                />
                {toggleButton(t)}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Все свои VPN-туннели -- вход на экран каждого: там удаление. Цепочка
          и «не используются» выше отвечают на другие вопросы и не содержат
          всех VPN-туннелей сразу. */}
      {list.length > 0 && (
        <Section title={`${TUNNEL_TEXTS.listTitle} · ${list.length}`}>
          <ul class="card list-reset">
            {list.map((t) => (
              <ListRow
                key={t.id}
                title={t.name}
                sub={`${t.stateLabel} · ${tunnelRuleSummary(t)}`}
                onClick={() => openLayer?.('tunnel', { tunnelID: t.id })}
              />
            ))}
          </ul>
        </Section>
      )}

      {/* Строка не зависит от несущего VPN-туннеля: без него «Маршруты» --
          единственный путь к ним и к HydraRoute Neo (финал п. 2). */}
      <ul class="card list-reset tunnels-more" style="margin-top:12px">
        <ListRow title="Маршруты: куда идёт трафик" sub={view.active ? `${view.active.rules} назн.` : undefined} onClick={onOpenRoutes} />
      </ul>

      {layer === 'tunnel' && (
        <TunnelScreen
          routerID={routerID}
          asleep={asleep}
          snapshot={snapshot}
          tunnelID={layerParams.tunnelID}
          role={role}
          openSheet={openSheet}
          onClose={closeLayer}
          onChanged={() => run('route_status', {}, deadline)}
          onOpenRebind={onOpenRebind}
          onRestart={() => run('route_status', {}, deadline)}
          canReplace={Boolean(view.active && view.policyName && view.active.id === layerParams.tunnelID)}
          onReplace={() => openLayer('replace', { tunnel: view.active, policyName: view.policyName, returnTo: 'tunnel', returnParams: { tunnelID: layerParams.tunnelID } })}
        />
      )}

      {layer === 'replace' && layerParams.tunnel && (
        <ReplaceScreen
          routerID={routerID}
          tunnel={layerParams.tunnel}
          policyName={layerParams.policyName}
          onClose={closeLayer}
          onDone={() => run('route_status', {}, deadline)}
          onOpenCabinet={() => openLayer('cabinet', {})}
          onOpenTunnel={(tunnelID) => (tunnelID ? openLayer('tunnel', { tunnelID }) : closeLayer())}
        />
      )}

      {layer === 'confimport' && (
        <ConfImportScreen
          routerID={routerID}
          asleep={asleep}
          snapshot={snapshot}
          onClose={closeLayer}
          onImported={() => run('route_status', {}, deadline)}
        />
      )}
    </div>
  )
}
