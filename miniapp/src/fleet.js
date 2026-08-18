// Список роутеров сортируется по срочности, а не по id: человек открывает
// список, когда что-то сломалось, и сломанное должно быть сверху.
const URGENCY = { alert: 0, offline: 1, sleeping: 2, online: 3 }

export function sortByUrgency(routers = []) {
  return [...routers].sort((a, b) => {
    const ua = URGENCY[a.status] ?? 99
    const ub = URGENCY[b.status] ?? 99
    if (ua !== ub) return ua - ub
    return (a.nickname ?? '').localeCompare(b.nickname ?? '', 'ru')
  })
}
