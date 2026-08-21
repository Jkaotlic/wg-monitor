// Раскладка экрана «Туннели»: какая линия несёт трафик, кто подхватит, если
// она замолчит, и что не используется вовсе.
//
// Экран отвечает на вопросы в том порядке, в каком их задаёт оператор:
// сначала "работает ли сейчас", потом "что будет, если ляжет", и только
// потом "что вообще есть". Поэтому и раскладка считается тремя кусками, а не
// одним списком туннелей: список не отвечает ни на один из трёх вопросов.
import { tunnelLive, tunnelSwitchedOff } from './routes.js'

// Роль звена в цепочке. Различать "готов подхватить" и "выключен" обязательно:
// первое -- обещание, что трафик переживёт падение активной линии, второе --
// прямо противоположное. Выдать одно за другое цветом значило бы соврать в
// том единственном месте, ради которого резерв и заводят.
//
// Упавшее звено -- третья роль, а не разновидность выключенного. Раньше всё,
// что не поднято, называлось «выключен вручную»: экран докладывал о чужом
// решении там, где случилась поломка, и подсовывал кнопку «включить» линии,
// которая и так включена. Различение бесплатное: enabled -- это настройка,
// status -- факт, и они приходят порознь.
function chainRole(link, tunnel, activeTunnelID) {
  if (link.tunnel_id && link.tunnel_id === activeTunnelID) return 'active'
  const live = tunnelLive(tunnel ?? {})
  if (live === 'up') return 'ready'
  if (tunnel && tunnelSwitchedOff(tunnel)) return 'off'
  if (live === 'unknown') return 'unknown'
  return 'down'
}

// Значение справа не повторяет заголовок строки: "Работает сейчас" и рядом
// ещё раз "работает сейчас" -- это один факт, сказанный дважды, и второй раз
// не добавляет ничего. У активного звена справа стоит его возраст связи,
// у остальных -- то, чем они отличаются друг от друга.
const ROLE_NOTE = {
  active: '',
  ready: 'отвечает',
  down: 'включён',
  off: 'выключен',
  unknown: 'роутер не сказал',
}

export function tunnelsView(snapshot) {
  const empty = { active: null, policyName: '', chain: [], unused: [] }
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  if (tunnels.length === 0) return empty

  const byID = new Map(tunnels.map((t) => [t.id, t]))

  // Ведущей считается политика, чьё активное звено -- наш туннель. Политика,
  // уходящая мимо VPN, живой линией не является: её звено -- провайдер, и
  // карточка "линия поднята" на нём была бы неправдой.
  const policy = policies.find((p) => p.active_tunnel_id && byID.has(p.active_tunnel_id))
  if (!policy) return { ...empty, unused: [] }

  const activeTunnel = byID.get(policy.active_tunnel_id)
  const active = {
    id: activeTunnel.id,
    name: activeTunnel.name || activeTunnel.id,
    iface: activeTunnel.iface ?? '',
    live: tunnelLive(activeTunnel),
    handshakeAgeSec: activeTunnel.has_handshake ? (activeTunnel.handshake_age_sec ?? null) : null,
    rules: (policy.dns ?? 0) + (policy.static ?? 0),
  }

  const chain = (policy.interfaces ?? []).map((link) => {
    const tunnel = link.tunnel_id ? byID.get(link.tunnel_id) : undefined
    const role = chainRole(link, tunnel, policy.active_tunnel_id)
    const age = tunnel?.has_handshake ? (tunnel.handshake_age_sec ?? null) : null
    return {
      tunnelID: link.tunnel_id ?? '',
      name: link.name || link.bind,
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
    .map((t) => ({ id: t.id, name: t.name || t.id, live: tunnelLive(t), ndmsName: t.ndms_name ?? '' }))

  return { active, policyName: policy.name ?? '', chain, unused }
}
