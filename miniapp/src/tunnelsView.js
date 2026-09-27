// Раскладка экрана «VPN-туннели»: какой из них несёт трафик, кто подхватит,
// если он замолчит, и что не используется вовсе.
//
// Экран отвечает на вопросы в том порядке, в каком их задаёт оператор:
// сначала "работает ли сейчас", потом "что будет, если ляжет", и только
// потом "что вообще есть". Поэтому и раскладка считается тремя кусками, а не
// одним списком туннелей: список не отвечает ни на один из трёх вопросов.
import { tunnelLive, tunnelSwitchedOff, tunnelRows } from './routes.js'

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
function chainRole(link, tunnel, activeTunnelID) {
  const live = tunnelLive(tunnel ?? {})
  // Назначенный несущим, но мёртвый (проверка провалена, см. withCheckVerdict):
  // трафик в него уходит и теряется -- «Работает сейчас» было бы неправдой.
  if (link.tunnel_id && link.tunnel_id === activeTunnelID) {
    if (live === 'down') return 'activeDown'
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
}

// Имя VPN-туннеля глазами человека. Пустое имя -- это отсутствие имени, а не повод
// подставить идентификатор: «VPN-туннель без имени» честнее, чем «awg11», и не
// притворяется, что awg11 кто-то так назвал.
function lineTitle(name) {
  const clean = (name ?? '').trim()
  return clean === '' ? 'VPN-туннель без имени' : clean
}

export function tunnelsView(snapshot) {
  const empty = { active: null, policyName: '', chain: [], unused: [] }
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  if (tunnels.length === 0) return empty

  const byID = new Map(tunnels.map((t) => [t.id, t]))

  // Ведущей считается политика, чьё активное звено -- наш туннель. Политика,
  // уходящая мимо VPN, живым VPN-туннелем не является: её звено -- провайдер, и
  // карточка "VPN-туннель поднят" на нём была бы неправдой.
  const policy = policies.find((p) => p.active_tunnel_id && byID.has(p.active_tunnel_id))
  if (!policy) return { ...empty, unused: [] }

  const activeTunnel = byID.get(policy.active_tunnel_id)
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
    live: tunnelLive(activeTunnel),
    checkUnknown: Boolean(activeTunnel.verdict_unknown),
    handshakeAgeSec: activeTunnel.has_handshake ? (activeTunnel.handshake_age_sec ?? null) : null,
    // Правила политики + свои DNS и статические маршруты туннеля (counts):
    // у политики поля static нет (wire.RoutePolicySummary), статические
    // маршруты агент считает на туннель (MINI-09). Число -- то же, что в
    // строке туннеля раскладки (tunnelRows).
    rules: tunnelRows(snapshot).find((r) => r.id === activeTunnel.id)?.total ?? (policy.dns ?? 0),
  }

  const chain = (policy.interfaces ?? []).map((link) => {
    const tunnel = link.tunnel_id ? byID.get(link.tunnel_id) : undefined
    const role = chainRole(link, tunnel, policy.active_tunnel_id)
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

  const inChain = new Set(chain.map((c) => c.tunnelID).filter(Boolean))
  // Только свои туннели: WAN и системные записи каталога NDMS сюда не
  // попадают -- предложить поднять провайдера было бы бессмысленно.
  const unused = tunnels
    .filter((t) => t.type === 'managed' && !inChain.has(t.id))
    .map((t) => ({
      id: t.id,
      name: t.name || t.id,
      title: lineTitle(t.name),
      code: t.id,
      live: tunnelLive(t),
      ndmsName: t.ndms_name ?? '',
    }))

  return { active, policyName: policy.name ?? '', chain, unused }
}
