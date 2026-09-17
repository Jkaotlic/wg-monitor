// Кабинеты роутера: вкладки, ключи Amnezia Premium и коды HideMy.name, права,
// тексты листов и отказов. Секрет здесь не хранится: значение поля живёт в
// Sheet.jsx, а сюда приходит снимок -- только чтобы собрать тело запроса.

export const CABINET_KINDS = ['amnezia', 'hidemy']

// Провайдер в /vpn и /vpn/issue назван иначе, чем кабинет в /cabinets
// (hidemyname против hidemy): так сложилось раньше, и переименование
// сломало бы уже открытые у людей приложения.
export const VPN_PROVIDER = { amnezia: 'amnezia', hidemy: 'hidemyname', selfhosted: 'selfhosted' }

export function cabinetTabs(cabinets) {
  const tabs = [
    { id: 'amnezia', title: 'Amnezia' },
    { id: 'hidemy', title: 'HideMy' },
  ]
  // Свой сервер -- только админу, и решает это сервер: available приходит
  // true только ему. Любое другое значение -- вкладки нет.
  if (cabinets?.selfhosted?.available === true) tabs.push({ id: 'selfhosted', title: 'Свой сервер' })
  return tabs
}

export function pickTab(tabs, want) {
  return (tabs ?? []).some((t) => t.id === want) ? want : tabs?.[0]?.id ?? 'amnezia'
}

const FULL = new Set(['admin', 'owner'])
const MANAGE = new Set(['admin', 'owner', 'operator'])

// Права по спеке (решение 1): смотреть, добавить, выбрать активный -- все
// трое; удалить, отозвать, прислать .conf -- админ и владелец. Граница
// доступа -- сервер; здесь только то, какие кнопки рисовать.
export function cabinetPerms(role) {
  const full = FULL.has(role)
  return { manage: MANAGE.has(role), remove: full, revoke: full, sendConf: full }
}

const KIND = {
  amnezia: {
    noun: 'ключ',
    listKey: 'keys',
    sectionTitle: 'Ключи кабинета',
    addButton: 'Добавить ключ',
    addTitle: 'Добавить ключ Amnezia Premium',
    addBody: 'Скопируйте ключ из кабинета Amnezia Premium (строка начинается с vpn://) и вставьте сюда. Сервер проверит его входом в кабинет и сохранит только при успехе.',
    fieldLabel: 'Ключ vpn://',
    placeholder: 'vpn://…',
    empty: 'Ключ кабинета Amnezia Premium ещё не добавлен. Добавьте его — и здесь появятся страны, которые можно выпустить.',
  },
  hidemy: {
    noun: 'код',
    listKey: 'codes',
    sectionTitle: 'Коды доступа',
    addButton: 'Добавить код',
    addTitle: 'Добавить код HideMy.name',
    addBody: 'Вставьте код доступа HideMy.name. Сервер проверит его запросом списка серверов и сохранит только при успехе.',
    fieldLabel: 'Код доступа',
    placeholder: '',
    empty: 'Код доступа HideMy.name ещё не добавлен. Добавьте его — и здесь появятся серверы, которые можно выпустить.',
  },
}

export function kindText(kind) {
  return KIND[kind]
}

function capital(s) {
  return s ? s[0].toUpperCase() + s.slice(1) : s
}

// Маска -- последние знаки секрета. Сервер может прислать их голыми или со
// своими звёздочками; показываем одинаково: «…a1b2».
export function maskText(mask) {
  const tail = String(mask ?? '').replace(/^[\s*•.…]+/, '')
  return tail ? `…${tail}` : ''
}

export function secretRows(cabinets, kind) {
  const k = KIND[kind]
  const list = k ? cabinets?.[kind]?.[k.listKey] : null
  const rows = (Array.isArray(list) ? list : []).map((item) => {
    const mask = maskText(item?.mask)
    return {
      id: String(item?.id ?? ''),
      title: item?.label || `${capital(k.noun)} без подписи`,
      sub: [mask ? `${k.noun} ${mask}` : '', item?.active === true ? 'активный' : ''].filter(Boolean).join(' · '),
      active: item?.active === true,
    }
  })
  // Активный -- первым: это тот, из которого выпускается VPN-туннель.
  return rows.sort((a, b) => Number(b.active) - Number(a.active))
}

// Поле секрета -- пароль: Sheet.jsx рисует его type=password и
// autocomplete=new-password и стирает при отправке. Подпись секретом не
// является и переживает отказ сервера (keep), секрет -- нет.
export function addSecretFields(kind) {
  const k = KIND[kind]
  return [
    { name: 'secret', label: k.fieldLabel, type: 'password', placeholder: k.placeholder },
    { name: 'label', label: 'Подпись (необязательно)', placeholder: 'например, основной', keep: true },
  ]
}

export function addSecretReady(kind) {
  return (values) => {
    const v = String(values?.secret ?? '').trim()
    if (kind === 'amnezia') return v.startsWith('vpn://') && v.length > 'vpn://'.length
    return v.length > 0
  }
}

export function addSecretRequest(values) {
  return { secret: String(values?.secret ?? '').trim(), label: String(values?.label ?? '').trim() }
}

const GENERIC = 'Не получилось. Попробуйте ещё раз.'

const SECRET_ERRORS = {
  amnezia: {
    invalid_key: 'Это не ключ Amnezia Premium: ключ начинается с vpn:// и копируется из кабинета целиком.',
    cabinet_rejected: 'Кабинет Amnezia Premium не принял ключ. Проверьте, что он скопирован целиком и подписка активна.',
  },
  hidemy: {
    invalid_code: 'Это не похоже на код доступа HideMy.name.',
    cabinet_rejected: 'HideMy.name не принял код. Проверьте, что он скопирован целиком и подписка не закончилась.',
  },
}

export function cabinetErrorText(kind, err) {
  if (err?.serverMessage) return err.serverMessage
  if (err?.code === 'not_found') return 'Этого уже нет — откройте экран заново.'
  return SECRET_ERRORS[kind]?.[err?.code] || GENERIC
}

export function addDoneText(kind) {
  return `${capital(KIND[kind].noun)} сохранён.`
}

export function deleteDoneText(kind) {
  return `${capital(KIND[kind].noun)} удалён.`
}

export function activeDoneText(kind, row) {
  return `Активный ${KIND[kind].noun} — «${row.title}».`
}

export function deleteSecretSheetText(kind, row) {
  const k = KIND[kind]
  return {
    title: `Удалить ${k.noun} «${row.title}»?`,
    body: row.active
      ? `Это активный ${k.noun}: пока не выберете другой, выпускать VPN-туннели из кабинета не получится. Уже выпущенные VPN-туннели на роутере продолжат работать.`
      : `${capital(k.noun)} удалится с сервера. Уже выпущенные VPN-туннели на роутере продолжат работать.`,
  }
}

export function revokeSheetText(option, routerName) {
  return {
    title: `Отозвать «${option.label}»?`,
    body: `Выпущенный конфиг этой страны перестанет работать везде, где он стоит, и в подписке освободится место. VPN-туннель на роутере «${routerName}» останется, но трафик через него не пойдёт.`,
  }
}

export function revokeDoneText(option) {
  return `Конфиг «${option.label}» отозван, место в подписке свободно.`
}

export function revokeErrorText(err) {
  if (err?.serverMessage) return err.serverMessage
  if (err?.code === 'confirm_mismatch') return 'Имя роутера набрано не так.'
  return GENERIC
}

const ISSUE_BASE = 'через приложение он не проходит. На роутере появится новый VPN-туннель; прежние остаются на месте.'

export function issueExplain(pending) {
  const base =
    pending?.provider === 'selfhosted'
      ? `Сервер создаст на «${pending.option.label}» нового клиента и сразу отдаст конфиг роутеру — ${ISSUE_BASE}`
      : `Конфиг скачает сервер и сразу отдаст его роутеру — ${ISSUE_BASE}`
  return pending?.option?.note ? `${base} Этот конфиг уже выпускался: он будет перевыпущен, и старый перестанет работать.` : base
}

// Свой сервер выбирается целиком -- «варианта» внутри него нет, поэтому и
// option, и instance -- id сервера (сверка С7/С8 с частью 1).
export function issueArgs(pending) {
  const own = pending?.provider === 'selfhosted'
  return {
    provider: pending?.provider ?? '',
    option: own ? String(pending.instanceID ?? '') : String(pending?.option?.id ?? ''),
    instanceID: own ? String(pending.instanceID ?? '') : '',
  }
}

// Старые коды выпуска (до цикла 3) несут английский message: показывать его
// человеку нельзя. cabinet_failed -- слова кабинета, как было; остальные --
// своя фраза. Новые коды (свой сервер, личка) говорят по-русски сами.
const ISSUE_LEGACY_CODES = new Set(['unknown_provider', 'missing_option', 'bad_json'])

export function issueFailure(err, perms) {
  if (err?.code === 'slot_busy') {
    const canRevoke = Boolean(perms?.revoke)
    return {
      text: canRevoke
        ? 'Свободных мест в подписке нет. Отзовите одну из выпущенных стран — и выпуск пройдёт.'
        : 'Свободных мест в подписке нет. Отозвать выпущенную страну может владелец роутера или администратор.',
      offerRevoke: canRevoke,
    }
  }
  const generic = 'Не получилось выпустить конфиг. Попробуйте ещё раз.'
  let text = generic
  if (err?.serverMessage && !ISSUE_LEGACY_CODES.has(err.code)) {
    text = err.code === 'cabinet_failed' ? `Кабинет отказал: ${err.serverMessage}` : err.serverMessage
  }
  return { text, offerRevoke: false }
}

export function sendConfSheetText(pending) {
  const own = pending?.provider === 'selfhosted'
  return {
    title: 'Прислать .conf в личку?',
    body: `Бот пришлёт файл конфига «${pending.option.label}» вам в личные сообщения.${own ? ' На сервере для этого будет создан ещё один клиент.' : ''}`,
    note: 'В файле приватный ключ: у кого файл, у того и доступ к VPN. Не пересылайте его и не храните в общих чатах.',
  }
}

export const SEND_CONF_DONE = 'Файл отправлен вам в личку.'

export function sendConfErrorText(err) {
  if (err?.code === 'dm_unreachable') return err.serverMessage || 'Бот не может написать вам — откройте бота, нажмите /start и повторите.'
  return err?.serverMessage || GENERIC
}

export const CABINET_TEXTS = {
  title: 'Кабинеты VPN',
  loading: 'Читаем кабинеты…',
  loadError: 'Не удалось прочитать кабинеты. Откройте экран заново.',
  accountLoading: 'Спрашиваем кабинет…',
  accountError: 'Кабинет не ответил. Откройте экран заново.',
  instancesLoading: 'Читаем список серверов…',
  instancesError: 'Не удалось прочитать список своих серверов.',
  backToList: 'Выбрать, что отозвать',
  issueRunning: 'Выпускаем конфиг, роутер его принимает…',
}
