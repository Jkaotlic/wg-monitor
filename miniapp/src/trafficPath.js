// Состояния схемы пути трафика.
//
// Схема заменила абстрактный прибор с портами DNS/EXT/HR/AWGM/AGEN: тот
// рисовал внутренние проверки как разъёмы железки, требовал легенду под собой
// и всё равно не отвечал на вопрос, ради которого человек открывает
// приложение. Схема отвечает: вот твои устройства, вот роутер, а дальше два
// потока -- заблокированное через туннель, остальное напрямую.
//
// Два потока -- не украшение, а суть: единого маршрута «весь трафик туда-то»
// в этой системе не существует, и рисовать одну стрелку значило бы врать.

// Ветка считается живой только по факту. «Не знаем» -- полноценный третий
// ответ: у молчащего роутера все показания вчерашние, и рисовать по ним
// зелёное значит выдавать прошлое за настоящее.
// Несущий VPN-туннель обхода -- тот, кого назвал сервер (v0.56, спека B1):
// traffic.carrier_tunnel_id, посчитанный одной функцией для «Роутера» и
// «VPN-туннелей» (miniappCarrier). Бэкенд старше v0.56 поля carrier_basis не
// шлёт -- тогда несущим остаётся его главный выход egress_tunnel_id.
//
// Угадывания «первый поднятый» больше нет: 18.09 workrouter схема взяла
// первый running (мёртвое запасное звено) и покрасила обход красным, а в
// песочнице sandbox-broken «Роутер» называл vpn-de, вкладка -- vpn-nl.
// Несущий не назван -- не называем никого.
export function carrierID(traffic) {
  if (traffic?.carrier_basis) return traffic.carrier_tunnel_id ?? ''
  return traffic?.egress_tunnel_id ?? ''
}

// Сервер сказал, что несущий не отвечает (поднят, а проверка провалена, или
// не поднят вовсе): «работает» о нём говорить нельзя.
export function carrierDeadByServer(traffic) {
  return Boolean(traffic?.carrier_basis) && Boolean(traffic?.carrier_tunnel_id) && traffic.carrier_alive === false
}

// Несущий по снимку маршрутов роутера (route_status), когда сервер его не
// знает (carrier_basis none). active_tunnel_id политики в снимке -- слово
// самого роутера, а не догадка: «первым поднятым» оно не является (ревью
// B1). Политик через VPN несколько -- та, что несёт больше правил, как у
// сервера. sing-box и «напрямую» не трогаем: там несущего нет. Живость
// сервер не сказал (carrier_alive не задан) -- её решают проверки туннеля.
// Считаются только ИСПОЛНЯЕМЫЕ правила, как у сервера (miniappPolicyExecuted):
// правила HydraRoute Neo -- лишь при запущенном HydraRoute Neo. Политика без
// исполняемых правил ничего не несёт, и называть её звено несущим нельзя (M1).
function executedRules(p, snapshot) {
  const dns = p?.dns ?? 0
  return snapshot?.hr_neo?.running === true ? dns : dns - (p?.hr_neo ?? 0)
}

export function withSnapshotCarrier(traffic, snapshot) {
  if (!traffic || traffic.carrier_basis !== 'none' || traffic.mode === 'singbox' || traffic.mode === 'direct') return traffic
  const ids = new Set((Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []).map((t) => t.id))
  let best = null
  let bestN = 0
  for (const p of Array.isArray(snapshot?.policies) ? snapshot.policies : []) {
    if (!p?.active_tunnel_id || p.via_vpn === false || !ids.has(p.active_tunnel_id)) continue
    const n = executedRules(p, snapshot)
    if (n > bestN) {
      best = p
      bestN = n
    }
  }
  if (!best) return traffic
  const { carrier_alive: _alive, ...rest } = traffic
  return { ...rest, carrier_tunnel_id: best.active_tunnel_id, carrier_basis: 'snapshot' }
}

export function carrierLine({ traffic, tunnels }) {
  const id = carrierID(traffic)
  if (!id) return null
  return tunnels?.find((x) => x.tunnel_id === id) ?? null
}

// Жив ли VPN-туннель -- по слову САМОГО РОУТЕРА (run_state), а не по вердикту
// проверки: `status` в проекции несёт «ok|fail» конечного автомата, и путать
// их значит рисовать зелёную ветку там, где VPN-туннель остановлен.
function isRunning(t) {
  return t?.run_state === 'running'
}

// Годен ли VPN-туннель нести трафик: поднят, проверка не провалена и тревоги
// по нему нет. Поднятый, но не отвечающий (обмен ключами 24 минуты назад) --
// не резерв, а видимость резерва.
export function isAlive(t, incidents = []) {
  return isRunning(t) && t.status !== 'fail' && !incidents?.some((i) => i.check_name === `tunnel_${t.tunnel_id}`)
}

// Несущий назван роутером, а не выбран нами.
export function carrierKnown({ traffic, tunnels }) {
  return carrierLine({ traffic, tunnels }) != null
}

// Несущий жив: и по своей проверке и тревогам (isAlive), и по слову сервера.
export function carrierAlive({ traffic, tunnels, incidents = [] }) {
  const line = carrierLine({ traffic, tunnels })
  return line != null && isAlive(line, incidents) && !carrierDeadByServer(traffic)
}

// Мёртвый -- тот, кто должен работать, но не работает: тревога по нему или
// поднятый с проваленной проверкой. Выключенный руками (stopped/disabled без
// тревоги) не мёртв: трафик на него и не рассчитан.
export function isDead(t, incidents = []) {
  if (incidents?.some((i) => i.check_name === `tunnel_${t.tunnel_id}`)) return true
  return isRunning(t) && t.status === 'fail'
}

// Ветка VPN без названного несущего (sing-box, старый агент): имени нет, но
// состояние сказать можно, если все VPN-туннели говорят одно -- живые без
// мёртвых или мёртвые без живых. Вперемешку -- любой ответ был бы
// угадыванием, какой из них несёт.
function unnamedBranch({ tunnels, incidents }) {
  const alive = (tunnels ?? []).some((t) => isAlive(t, incidents))
  const dead = (tunnels ?? []).some((t) => isDead(t, incidents))
  if (alive && !dead) return 'up'
  if (dead && !alive) return 'down'
  return 'unknown'
}

function tunnelBranch({ line, traffic, tunnels, incidents, stale }) {
  if (stale) return 'unknown'
  if (!line) return unnamedBranch({ tunnels, incidents })
  // То же правило живости, что у шапки (carrierAlive): проваленная проверка
  // несущего -- уже «молчит», даже пока тревога не набрала порог. Иначе
  // схема рисовала бы зелёное рядом с шапкой, где этот туннель мёртв.
  return carrierAlive({ traffic, tunnels, incidents }) ? 'up' : 'down'
}

// Живые запасные звенья политики несущего по слову бэкенда, или null, если
// бэкенд их не считал. Поле omitempty: при раздельной маршрутизации с
// названным несущим его отсутствие значит «живых запасных нет» -- так
// бэкенд отвечает и старым агентам, у которых несущий назван потому, что
// живой туннель один.
export function reserveIDs({ traffic, tunnels }) {
  if (Array.isArray(traffic?.reserve_tunnel_ids)) return traffic.reserve_tunnel_ids
  if (traffic?.mode === 'split' && carrierKnown({ traffic, tunnels })) return []
  return null
}

// Запасной VPN-туннель -- ответ на «а если этот ляжет». Новый бэкенд знает
// запасные звенья политики несущего и отдаёт живые в reserve_tunnel_ids: им и
// верим. Без поля (старый агент) -- любой ЖИВОЙ туннель, кроме несущего; когда
// несущий не назван, резерв есть, только если живых больше одного, но назвать
// его -- угадать ({ tunnel_id: '' }). Раньше резервом считался любой running,
// и мёртвое звено объявлялось «готовым подхватить».
export function reserveLine({ traffic, tunnels = [], incidents = [], via = '' }) {
  const ids = reserveIDs({ traffic, tunnels })
  if (ids) return tunnels.find((t) => ids.includes(t.tunnel_id))
  const alive = tunnels.filter((t) => isAlive(t, incidents))
  if (via) return alive.find((t) => (t.name || t.tunnel_id) !== via && t.tunnel_id !== carrierID(traffic))
  return alive.length > 1 ? { tunnel_id: '' } : undefined
}

// Запасной, который есть, но мёртв (тревога по нему или поднят с проваленной
// проверкой), -- не то же, что «запасного нет» (v0.50, спека п. 1.3).
// Несущий исключается по id и по имени, которое схема написала на ветке.
// Выключенный руками мёртвым не считается: на него трафик и не рассчитан.
export function deadReserveLine({ traffic, tunnels = [], incidents = [], via = '' }) {
  const carrier = carrierID(traffic)
  return tunnels.find((t) => t.tunnel_id !== carrier && (t.name || t.tunnel_id) !== via && isDead(t, incidents))
}

// Слова строки резерва под схемой. Несущий молчит, а запасной жив -- «готов,
// подхватит» было бы неправдой: у opkg-туннелей автофолбэка нет, политика
// сама на запасной не уйдёт. Уводит трафик «Починить» (движок починки сам
// роняет мёртвое звено, и политика переходит на резерв).
// Шапка закрывает плитку резерва, только когда её тревога -- про сам упавший
// запасной. Несущий упал, а запасной «поплыл» без своей тревоги -- шапка про
// несущего, и о запасном не говорит ничего: плитка обязана.
export function heroCoversReserve(deadReserve, headlineCheck) {
  return deadReserve != null && headlineCheck === `tunnel_${deadReserve.tunnel_id}`
}

// deadReserve -- запасной, который упал; heroCovers -- шапка экрана уже
// говорит о нём (тревога по VPN-туннелю). Тогда плитки нет вовсе: четыре
// вердикта про одно и то же -- это шум, а не подробность.
export function backupCopy({ backupLine, carrierDown = false, deadReserve = null, heroCovers = false }) {
  if (!backupLine && deadReserve) {
    if (heroCovers) return null
    const dead = deadReserve.name ? `«${deadReserve.name}»` : ''
    return {
      title: dead ? `Запасной ${dead} не отвечает` : 'Запасной VPN-туннель не отвечает',
      note: 'если основной ляжет, подхватить будет некому — почините запасной',
      tone: 'warn',
    }
  }
  if (!backupLine) {
    return { title: 'Запасного VPN-туннеля нет', note: 'если VPN-туннель ляжет, обход блокировок пропадёт до починки', tone: 'warn' }
  }
  const named = backupLine.name ? `«${backupLine.name}»` : ''
  if (carrierDown) {
    return {
      title: named ? `Запасной ${named} жив` : 'Запасной VPN-туннель жив',
      note: 'но сам трафик на него не перейдёт — нажмите «Починить» в тревоге',
      tone: 'warn',
    }
  }
  return {
    title: 'Запасной VPN-туннель готов',
    note: named ? `${named} подхватит, если этот замолчит` : 'второй VPN-туннель подхватит, если один замолчит',
    tone: 'ok',
  }
}

// Имя несущего на экране: своё имя VPN-туннеля, а без него -- имя, которое
// сервер дал главному выходу, и только потом идентификатор.
export function lineVia(t, traffic) {
  if (!t) return ''
  const egressName = t.tunnel_id === traffic?.egress_tunnel_id ? traffic?.egress_tunnel_name : ''
  return t.name || egressName || t.tunnel_id
}

export function pathState({ traffic, incidents = [], tunnels = [], stale = false } = {}) {
  const t = carrierLine({ traffic, tunnels })
  const tunnel = tunnelBranch({ line: t, traffic, tunnels, incidents, stale })
  return {
    tunnel,
    // Прямой поток не зависит от туннеля: он идёт мимо. Гасить его вместе с
    // упавшим VPN-туннелем значило бы говорить человеку «интернета нет», когда
    // банки и госуслуги у него работают.
    direct: stale ? 'unknown' : 'up',
    // Подпись ветки -- только названный несущий: подписать её первым
    // попавшимся именем и его задержкой значило бы угадать.
    via: lineVia(t, traffic),
    latencyMs: t && typeof t.matrix_latency_ms === 'number' ? t.matrix_latency_ms : null,
  }
}
