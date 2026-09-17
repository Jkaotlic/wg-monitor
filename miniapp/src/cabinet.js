// Кабинеты провайдеров: что видно про подписку и что можно из неё выпустить.
//
// Ключи и коды кабинетов вводятся на экране кабинета и живут на сервере;
// сюда приезжает только то, что кабинет рассказал о себе. Содержимое конфига
// через клиента не ходит -- он передаёт лишь выбор.

// canRevoke -- видит ли человек «Отозвать» (админ и владелец). Без второго
// аргумента (мастер замены) текст говорит, кто может освободить место.
export function accountSummary(account, { canRevoke = false } = {}) {
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
      ? canRevoke
        ? 'Свободных мест в подписке нет. Освободите место — отзовите одну из выпущенных стран ниже, и выпуск откроется.'
        : 'Свободных мест в подписке нет. Освободить место может владелец роутера или администратор: для этого отзывается одна из выпущенных стран.'
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
    // issued -- ещё и признак «можно отозвать» в кабинете Amnezia.
    issued: o.issued === true,
  }))
}
