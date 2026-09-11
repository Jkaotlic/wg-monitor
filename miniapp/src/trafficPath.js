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
// Какой VPN-туннель несёт обход. Обычно его называет сам роутер (egress_tunnel_id),
// но на sing-box единого выхода нет: маршрут выбирается для каждого адреса.
// VPN-туннель при этом существует, и писать «роутер не сказал» над поднятым
// VPN-туннелем значило бы соврать -- берём первый работающий.
function activeLine({ traffic, tunnels }) {
  const named = tunnels?.find((x) => x.tunnel_id === traffic?.egress_tunnel_id)
  if (named) return named
  return tunnels?.find(isRunning) ?? null
}

// Жив ли VPN-туннель -- по слову САМОГО РОУТЕРА (run_state), а не по вердикту
// проверки: `status` в проекции несёт «ok|fail» конечного автомата, и путать
// их значит рисовать зелёную ветку там, где VPN-туннель остановлен.
function isRunning(t) {
  return t?.run_state === 'running'
}

function tunnelBranch({ line, incidents, stale }) {
  if (stale) return 'unknown'
  if (!line) return 'unknown'
  if (incidents?.some((i) => i.check_name === `tunnel_${line.tunnel_id}`)) return 'down'
  return isRunning(line) ? 'up' : 'down'
}

export function pathState({ traffic, incidents = [], tunnels = [], stale = false } = {}) {
  const t = activeLine({ traffic, tunnels })
  const tunnel = tunnelBranch({ line: t, incidents, stale })
  // При раздельной маршрутизации без названного выхода VPN-туннель выбирают
  // правила для каждого адреса. Ветка живая, но подписать её первым попавшимся
  // именем и его задержкой значило бы угадать.
  const guess = traffic?.mode === 'split' && t?.tunnel_id !== traffic?.egress_tunnel_id
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
