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
      ? 'Свободных мест в подписке нет: отзовите один из выпущенных конфигов в боте, чтобы освободить место.'
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
