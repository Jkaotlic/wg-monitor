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
// Какой VPN-туннель несёт обход. Роутер называет его сам (egress_tunnel_id):
// с v0.41 агент сообщает несущее звено политики и при раздельной
// маршрутизации. Без имени на sing-box единого выхода нет, маршрут выбирается
// для каждого адреса -- берём первый работающий, лучше исправный: писать
// «роутер не сказал» над поднятым VPN-туннелем значило бы соврать.
//
// Раздельная маршрутизация без имени (агент старше v0.41) и среди туннелей
// есть мёртвые -- не угадываем вовсе. 18.09 workrouter: схема взяла первый
// running (мёртвое запасное звено) и покрасила ветку красным, хотя обход шёл
// через живой соседний.
export function carrierLine({ traffic, tunnels }) {
  const named = tunnels?.find((x) => x.tunnel_id === traffic?.egress_tunnel_id)
  if (named) return named
  return tunnels?.find((t) => isRunning(t) && t.status !== 'fail') ?? tunnels?.find(isRunning) ?? null
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
  return Boolean(traffic?.egress_tunnel_id) && Boolean(tunnels?.some((x) => x.tunnel_id === traffic.egress_tunnel_id))
}

// Несущий неизвестен, туннелей несколько и часть мертва: любой выбор -- угадывание.
function blindSplit({ traffic, tunnels, incidents }) {
  if (traffic?.mode !== 'split' || carrierKnown({ traffic, tunnels })) return false
  return (tunnels?.length ?? 0) > 1 && tunnels.some((t) => !isAlive(t, incidents))
}

function tunnelBranch({ line, incidents, stale }) {
  if (stale) return 'unknown'
  if (!line) return 'unknown'
  if (incidents?.some((i) => i.check_name === `tunnel_${line.tunnel_id}`)) return 'down'
  return isRunning(line) ? 'up' : 'down'
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
  if (via) return alive.find((t) => (t.name || t.tunnel_id) !== via && t.tunnel_id !== traffic?.egress_tunnel_id)
  return alive.length > 1 ? { tunnel_id: '' } : undefined
}

export function pathState({ traffic, incidents = [], tunnels = [], stale = false } = {}) {
  const blind = blindSplit({ traffic, tunnels, incidents })
  const t = blind ? null : carrierLine({ traffic, tunnels })
  const tunnel = tunnelBranch({ line: t, incidents, stale })
  // При раздельной маршрутизации без названного выхода VPN-туннель выбирают
  // правила для каждого адреса. Ветка живая, но подписать её первым попавшимся
  // именем и его задержкой значило бы угадать.
  const guess = blind || (traffic?.mode === 'split' && t?.tunnel_id !== traffic?.egress_tunnel_id)
  return {
    tunnel,
    // Прямой поток не зависит от туннеля: он идёт мимо. Гасить его вместе с
    // упавшим VPN-туннелем значило бы говорить человеку «интернета нет», когда
    // банки и госуслуги у него работают.
    direct: stale ? 'unknown' : 'up',
    via: guess ? '' : traffic?.egress_tunnel_name || t?.name || t?.tunnel_id || '',
    latencyMs: !guess && typeof t?.matrix_latency_ms === 'number' ? t.matrix_latency_ms : null,
  }
}
