// Кабинеты провайдеров: что видно про подписку и что можно из неё выпустить.
//
// Ключей кабинета приложение не спрашивает никогда: они живут у бота, и
// сюда приезжает только то, что кабинет рассказал о себе. Содержимое конфига
// через клиента тоже не ходит -- он передаёт лишь выбор.

export function accountSummary(account) {
  const label = account?.label || account?.provider || 'Кабинет'
  if (!account?.connected) {
    return {
      title: label,
      lines: [],
      canIssue: false,
      // Слова кабинета важнее наших: он знает, чего не хватает.
      reason: account?.note || 'Кабинет не подключён.',
    }
  }
  const lines = []
  if (account.status) lines.push(`Подписка: ${account.status}`)
  if (account.ends_at) lines.push(`Действует до ${account.ends_at}`)
  if (account.devices_max) lines.push(`Устройств занято ${account.devices_used ?? 0} из ${account.devices_max}`)

  const full = Boolean(account.devices_max) && (account.devices_used ?? 0) >= account.devices_max
  const hasOptions = (account.options ?? []).length > 0
  return {
    title: label,
    lines,
    canIssue: hasOptions && !full,
    reason: full
      // Отзыв живёт в панели бота, а она админская: после переезда
      // уведомлений в личку кнопок в темах не осталось вовсе. Владелец
      // роутера кабинет видит -- и посылать его туда, куда он не попадёт,
      // нельзя. Но и тупик без выхода не годится: говорим, ЧТО должно
      // произойти, не обещая, что это сделает он сам.
      ? 'Свободных мест в подписке нет. Чтобы выпустить ещё одну линию, надо освободить место — отозвать одну из выпущенных раньше. Это делает тот, у кого доступ к самой подписке.'
      : hasOptions
        ? ''
        : account.note || 'Кабинет не назвал, что можно выпустить.',
  }
}

export function optionRows(account) {
  return (account?.options ?? []).map((o) => ({
    id: o.id,
    label: o.label || o.id,
    // Выпустить то, что уже выпущено, можно -- конфиг перевыпустится, -- но
    // человек должен знать об этом до нажатия, а не после.
    note: o.issued ? 'уже выпущен' : '',
  }))
}
