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
function tunnelBranch({ traffic, incidents, tunnels, stale }) {
  if (stale) return 'unknown'
  const id = traffic?.egress_tunnel_id
  if (incidents?.some((i) => i.check_name === `tunnel_${id}`)) return 'down'
  const t = tunnels?.find((x) => x.tunnel_id === id)
  if (!t) return 'unknown'
  return t.status === 'running' ? 'up' : 'down'
}

export function pathState({ traffic, incidents = [], tunnels = [], stale = false } = {}) {
  const tunnel = tunnelBranch({ traffic, incidents, tunnels, stale })
  const t = tunnels.find((x) => x.tunnel_id === traffic?.egress_tunnel_id)
  return {
    tunnel,
    // Прямой поток не зависит от туннеля: он идёт мимо. Гасить его вместе с
    // упавшей линией значило бы говорить человеку «интернета нет», когда
    // банки и госуслуги у него работают.
    direct: stale ? 'unknown' : 'up',
    via: traffic?.egress_tunnel_name || traffic?.egress_tunnel_id || '',
    latencyMs: typeof t?.matrix_latency_ms === 'number' ? t.matrix_latency_ms : null,
  }
}
