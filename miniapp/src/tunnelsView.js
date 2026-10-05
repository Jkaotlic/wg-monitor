// Раскладка экрана «VPN-туннели»: какой из них несёт трафик, кто подхватит,
// если он замолчит, и что не используется вовсе.
//
// Экран отвечает на вопросы в том порядке, в каком их задаёт оператор:
// сначала "работает ли сейчас", потом "что будет, если ляжет", и только
// потом "что вообще есть". Поэтому и раскладка считается тремя кусками, а не
// одним списком туннелей: список не отвечает ни на один из трёх вопросов.
import { tunnelLive, tunnelSwitchedOff, tunnelRows } from './routes.js'
import { withSnapshotCarrier } from './trafficPath.js'

// Роль звена в цепочке. Различать "готов подхватить" и "выключен" обязательно:
// первое -- обещание, что трафик переживёт падение активного VPN-туннеля, второе --
// прямо противоположное. Выдать одно за другое цветом значило бы соврать в
// том единственном месте, ради которого резерв и заводят.
//
// Упавшее звено -- третья роль, а не разновидность выключенного. Раньше всё,
// что не поднято, называлось «выключен вручную»: экран докладывал о чужом
// решении там, где случилась поломка, и подсовывал кнопку «включить» VPN-туннелю,
// который и так включён. Различение бесплатное: enabled -- это настройка,
// status -- факт, и они приходят порознь.
function chainRole(link, tunnel, activeTunnelID, { singbox = false, carrierDead = false } = {}) {
  const live = tunnelLive(tunnel ?? {})
  // sing-box выбирает маршрут для каждого адреса: активное звено политики
  // настроено, но «несёт трафик» о нём сказать нельзя (B1).
  if (singbox && link.tunnel_id && link.tunnel_id === activeTunnelID) return 'routed'
  // Назначенный несущим, но мёртвый (проверка провалена, см. withCheckVerdict,
  // или сервер сказал carrier_alive=false): трафик в него уходит и теряется --
  // «Работает сейчас» было бы неправдой.
  if (link.tunnel_id && link.tunnel_id === activeTunnelID) {
    if (live === 'down' || carrierDead) return 'activeDown'
    // Проверки не загрузились (withCheckVerdict, verdict_unknown): несёт ли он
    // трафик на деле -- неизвестно, «Работает сейчас» было бы догадкой.
    return tunnel?.verdict_unknown ? 'activeUnknown' : 'active'
  }
  if (live === 'up') return 'ready'
  if (tunnel && tunnelSwitchedOff(tunnel)) return 'off'
  if (live === 'unknown') return tunnel?.verdict_unknown ? 'checkUnknown' : 'unknown'
  return 'down'
}

// Значение справа не повторяет заголовок строки: "Работает сейчас" и рядом
// ещё раз "работает сейчас" -- это один факт, сказанный дважды, и второй раз
// не добавляет ничего. У активного звена справа стоит его возраст связи,
// у остальных -- то, чем они отличаются друг от друга.
const ROLE_NOTE = {
  active: '',
  activeDown: 'трафик не проходит',
  ready: 'отвечает',
  down: 'включён',
  off: 'выключен',
  unknown: 'роутер не сказал',
  // Роутер сказал «поднят», но проверки с сервера не пришли (review v0.46,
  // п. 5): неизвестна проверка, а не слово роутера.
  activeUnknown: 'проверка не пришла: сервер не ответил',
  checkUnknown: 'поднят, проверка не пришла: сервер не ответил',
  routed: '',
}

// Имя VPN-туннеля глазами человека. Пустое имя -- это отсутствие имени, а не повод
// подставить идентификатор: «VPN-туннель без имени» честнее, чем «awg11», и не
// притворяется, что awg11 кто-то так назвал.
function lineTitle(name) {
  const clean = (name ?? '').trim()
  return clean === '' ? 'VPN-туннель без имени' : clean
}

function rulesNote(row, policy) {
  const name = policy?.name ?? ''
  const viaPolicy = row?.policyRules ?? (policy?.dns ?? 0)
  const own = row ? row.total - row.policyRules : 0
  if (own > 0) return name ? `${viaPolicy} из набора «${name}», ${own} своих` : `${viaPolicy} из общего набора, ${own} своих`
  return name ? `общий набор «${name}»` : ''
}

// Цепочка политики звеньями. activeTunnelID -- кого отметить несущим; пусто --
// никого (несущий неизвестен: звенья показываем, роль «работает сейчас» -- нет).
function chainOf(policy, byID, activeTunnelID, opts) {
  return (policy?.interfaces ?? []).map((link) => {
    const tunnel = link.tunnel_id ? byID.get(link.tunnel_id) : undefined
    const role = chainRole(link, tunnel, activeTunnelID, opts)
    const age = tunnel?.has_handshake ? (tunnel.handshake_age_sec ?? null) : null
    return {
      tunnelID: link.tunnel_id ?? '',
      name: link.name || link.bind,
      title: lineTitle(link.name),
      code: link.tunnel_id || link.bind,
      bind: link.bind,
      role,
      note: ROLE_NOTE[role],
      handshakeAgeSec: role === 'active' ? age : null,
      // Имя NDMS-интерфейса -- единственный способ включить или выключить
      // туннель (агент делает это ndmc'ом). Пусто у opkg-туннелей: их в NDMS
      // нет, и кнопки под ними быть не должно.
      ndmsName: tunnel?.ndms_name ?? '',
      live: tunnel ? tunnelLive(tunnel) : 'unknown',
    }
  })
}

// Только свои туннели: WAN и системные записи каталога NDMS сюда не
// попадают -- предложить поднять провайдера было бы бессмысленно.
function unusedOf(tunnels, chain) {
  const inChain = new Set(chain.map((c) => c.tunnelID).filter(Boolean))
  return tunnels
    .filter((t) => t.type === 'managed' && !inChain.has(t.id))
    .map((t) => ({
      id: t.id,
      name: t.name || t.id,
      title: lineTitle(t.name),
      code: t.id,
      live: tunnelLive(t),
      ndmsName: t.ndms_name ?? '',
    }))
}

// Ведущей считается политика, чьё активное звено -- наш туннель. Политика,
// уходящая мимо VPN, живым VPN-туннелем не является: её звено -- провайдер, и
// карточка "VPN-туннель поднят" на нём была бы неправдой.
function firstVPNPolicy(policies, byID) {
  return policies.find((p) => p.active_tunnel_id && byID.has(p.active_tunnel_id))
}

// tunnelsView -- раскладка вкладки. traffic -- ответ сервера
// (/routers/{id}/events: traffic) с несущим, посчитанным тем же правилом,
// что у экрана «Роутер» (B1, v0.56): carrier_tunnel_id + carrier_basis.
// Вкладка берёт политику, чьё активное звено = несущий, и не угадывает.
//
// state:
//   carrier -- несущий назван (active заполнен);
//   singbox -- маршрут выбирается для каждого адреса: политика «настроена»,
//              несущего нет;
//   unknown -- сервер несущего не назвал или проверки не загрузились;
//   none    -- ни одна политика не ведёт в VPN-туннель.
// Без traffic.carrier_basis (бэкенд старше v0.56, тесты раскладки) несущим
// остаётся активное звено первой VPN-политики снимка -- слово роутера.
export function tunnelsView(snapshot, serverTraffic) {
  const empty = { state: 'none', active: null, policyName: '', chain: [], unused: [] }
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  if (tunnels.length === 0) return empty

  const byID = new Map(tunnels.map((t) => [t.id, t]))
  // Сервер несущего не знает -- активное звено из снимка роутера, тем же
  // правилом, что у экрана «Роутер» (withSnapshotCarrier).
  const traffic = withSnapshotCarrier(serverTraffic, snapshot)

  if (traffic?.mode === 'singbox') {
    const p = firstVPNPolicy(policies, byID)
    const chain = chainOf(p, byID, p?.active_tunnel_id ?? '', { singbox: true })
    return { state: 'singbox', active: null, policyName: p?.name ?? '', chain, unused: unusedOf(tunnels, chain) }
  }

  let carrierID
  if (traffic?.carrier_basis) {
    carrierID = traffic.carrier_tunnel_id && byID.has(traffic.carrier_tunnel_id) ? traffic.carrier_tunnel_id : ''
    if (!carrierID) {
      // Несущего нет. «Ни один VPN-туннель не несёт трафик» -- только когда
      // и сервер говорит «напрямую»; иначе -- не знаем, и не гадаем (старый
      // агент без active_tunnel_id в политиках снимка -- тоже «не знаем»,
      // ревью B1: шапка «Роутера» на тех же данных говорит «всё работает»).
      const p = firstVPNPolicy(policies, byID) ?? policies.find((x) => x.via_vpn && (x.interfaces ?? []).some((l) => l.tunnel_id && byID.has(l.tunnel_id)))
      const chain = chainOf(p, byID, '')
      const state = traffic.mode === 'direct' ? 'none' : 'unknown'
      return { state, active: null, policyName: p?.name ?? '', chain, unused: unusedOf(tunnels, chain) }
    }
  } else {
    carrierID = firstVPNPolicy(policies, byID)?.active_tunnel_id ?? ''
    if (!carrierID) return { ...empty, unused: [] }
  }

  // Политика несущего: та, чьё активное звено -- он; снимок мог разойтись с
  // проверками сервера на один отчёт -- тогда та, где он хотя бы звено.
  const policy =
    policies.find((p) => p.active_tunnel_id === carrierID) ??
    policies.find((p) => (p.interfaces ?? []).some((l) => l.tunnel_id === carrierID))
  const carrierDead = Boolean(traffic?.carrier_basis) && traffic.carrier_alive === false

  const activeTunnel = byID.get(carrierID)
  const activeRow = tunnelRows(snapshot).find((r) => r.id === activeTunnel.id)
  const active = {
    id: activeTunnel.id,
    name: activeTunnel.name || activeTunnel.id,
    // title -- то, что читает человек; code -- то, что нужно инженеру и
    // командам. Раньше это было одно поле, и у безымянного VPN-туннеля в заголовок
    // вставал идентификатор: экран начинал говорить по-машинному ровно там,
    // где человек ищет ответ.
    title: lineTitle(activeTunnel.name),
    code: activeTunnel.id,
    iface: activeTunnel.iface ?? '',
    // Сервер сказал «не отвечает» -- карточка не скажет «поднят».
    live: carrierDead ? 'down' : tunnelLive(activeTunnel),
    checkUnknown: Boolean(activeTunnel.verdict_unknown),
    // Проверка пришла, но ничего не проверила (unknown, v0.46).
    unverified: Boolean(activeTunnel.check_unverified),
    handshakeAgeSec: activeTunnel.has_handshake ? (activeTunnel.handshake_age_sec ?? null) : null,
    // Правила политики + свои DNS и статические маршруты туннеля (counts):
    // у политики поля static нет (wire.RoutePolicySummary), статические
    // маршруты агент считает на туннель (MINI-09). Число -- то же, что в
    // строке туннеля раскладки (tunnelRows).
    rules: activeRow?.total ?? (policy?.dns ?? 0),
    // Подпись описывает то же число (review v0.46, п. 6): «общий набор» --
    // только когда своих правил у туннеля нет.
    rulesNote: rulesNote(activeRow, policy),
  }

  const chain = chainOf(policy, byID, carrierID, { carrierDead })
  return { state: 'carrier', active, policyName: policy?.name ?? '', chain, unused: unusedOf(tunnels, chain) }
}
