import { tunnelStateLabel } from '../labels.js'

// "Работает" считается ровно тем же словарём, которым подписан каждый туннель
// в списке: вторая формула для той же величины разошлась бы с первой.
export function tunnelHealth(tunnels = []) {
  const total = tunnels.length
  if (total === 0) return { alive: 0, total: 0, label: 'нет данных' }
  const alive = tunnels.filter((t) => tunnelStateLabel(t) === 'работает').length
  return { alive, total, label: `${alive} из ${total}` }
}
