// Обмен по туннелю: ряд ведёт сам роутер, агент приносит его вместе с
// суммами (wire.TunnelTraffic). Экран печатает суммы -- считать их второй
// раз здесь значило бы держать вторую формулу того же числа.

const UNITS = [
  { limit: 1024 ** 3, suffix: 'ГБ' },
  { limit: 1024 ** 2, suffix: 'МБ' },
  { limit: 1024, suffix: 'КБ' },
]

// Байты человек не читает. Десятичный знак -- один: «3,05 ГБ» точнее, но
// разница между 3,0 и 3,05 не меняет ни одного решения.
export function formatBytes(bytes) {
  if (bytes == null || Number.isNaN(bytes)) return ''
  for (const u of UNITS) {
    if (bytes >= u.limit) return `${(bytes / u.limit).toFixed(1).replace('.', ',')} ${u.suffix}`
  }
  return `${bytes} Б`
}

export function trafficSummary(output) {
  let data = null
  try {
    data = JSON.parse(output)
  } catch {
    return { known: false, rx: '', tx: '', points: 0, empty: false, period: '' }
  }
  if (!data || typeof data !== 'object' || typeof data.rx_total !== 'number') {
    return { known: false, rx: '', tx: '', points: 0, empty: false, period: '' }
  }
  const points = Array.isArray(data.points) ? data.points.length : 0
  return {
    known: true,
    tunnelID: data.tunnel_id ?? '',
    period: data.period ?? '',
    rx: formatBytes(data.rx_total),
    tx: formatBytes(data.tx_total ?? 0),
    points,
    // Ряд без точек -- это ответ «за период обмена не было»: роутер посчитал
    // и ничего не нашёл. Отличать его от «не спрашивали» обязательно.
    empty: points === 0,
  }
}
