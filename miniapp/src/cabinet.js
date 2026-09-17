// Кабинеты провайдеров: что видно про подписку и что можно из неё выпустить.
//
// Ключи и коды кабинетов вводятся на экране кабинета и живут на сервере;
// сюда приезжает только то, что кабинет рассказал о себе. Содержимое конфига
// через клиента не ходит -- он передаёт лишь выбор.

// canRevoke -- видит ли человек «Отозвать» (админ и владелец). Без второго
// аргумента (мастер замены) текст говорит, кто может освободить место.
export const FULL_NOTE = 'Свободных мест в подписке нет — выпуск новых стран закрыт.'
const FULL_AGAIN = 'Уже выпущенные можно выпустить заново.'

function isFull(account) {
  return Boolean(account?.devices_max) && (account.devices_used ?? 0) >= account.devices_max
}

// Полная подписка закрывает только НОВЫЕ страны: выпущенная уже занимает
// своё место, и сервер (amneziaSlotBusy) пускает её перевыпуск. canIssue --
// можно ли выпустить хоть что-то; какие именно -- optionRows().available.
export function accountSummary(account, { canRevoke = false } = {}) {
  const label = account?.label || account?.provider || 'Кабинет'
  if (!account?.connected) {
    return {
      title: label,
      lines: [],
      canIssue: false,
      full: false,
      fullNote: '',
      // Слова кабинета важнее наших: он знает, чего не хватает.
      reason: account?.note || 'Кабинет не подключён.',
    }
  }
  const lines = []
  if (account.status) lines.push(subscriptionLine(account.status))
  if (account.ends_at) lines.push(`Действует до ${account.ends_at}`)
  if (account.devices_max) lines.push(`Устройств занято ${account.devices_used ?? 0} из ${account.devices_max}`)

  const full = isFull(account)
  const options = account.options ?? []
  const hasOptions = options.length > 0
  const anyIssued = options.some((o) => o.issued === true)
  return {
    title: label,
    lines,
    canIssue: hasOptions && (!full || anyIssued),
    full,
    fullNote: full ? FULL_NOTE : '',
    reason: full
      ? canRevoke
        ? `${FULL_NOTE} ${FULL_AGAIN} Освободите место — отзовите одну из выпущенных стран ниже.`
        : `${FULL_NOTE} ${FULL_AGAIN} Освободить место может владелец роутера или администратор: для этого отзывается одна из выпущенных стран.`
      : hasOptions
        ? ''
        : account.note || 'Кабинет не назвал, что можно выпустить.',
  }
}

export function optionRows(account) {
  const full = isFull(account)
  return (account?.options ?? []).map((o) => ({
    id: o.id,
    label: o.label || o.id,
    // Выпустить то, что уже выпущено, можно -- конфиг перевыпустится, -- но
    // человек должен знать об этом до нажатия, а не после.
    note: o.issued ? 'уже выпущен' : '',
    // issued -- ещё и признак «можно отозвать» в кабинете Amnezia.
    issued: o.issued === true,
    // При полной подписке доступна только уже выпущенная страна.
    available: !full || o.issued === true,
  }))
}

// Кабинет присылает состояние подписки английским словом. Знакомые слова --
// по-русски; незнакомое показываем как есть, а не прячем.
const SUBSCRIPTION_WORDS = {
  active: 'Подписка активна',
  expired: 'Подписка закончилась',
  inactive: 'Подписка не активна',
}

function subscriptionLine(status) {
  return SUBSCRIPTION_WORDS[String(status).toLowerCase()] ?? `Подписка: ${status}`
}
