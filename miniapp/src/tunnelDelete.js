// Экран VPN-туннеля: можно ли его удалить и что сказать человеку. Экран
// TunnelScreen.jsx только рисует.
//
// Сервер проверяет всё сам по свежему снимку агента (правила, главный выход) и
// клиенту не верит. Клиент считает то же заранее и так же, как сервер
// (miniappTunnelRuleCount -- порт tunnelRows), чтобы не предлагать кнопку,
// которая заведомо получит отказ, -- и говорит теми же словами, что при отказе.
import { tunnelRows } from './routes.js'
import { rulesCount } from './labels.js'
import { commandOutcome } from './commandWait.js'

export const TUNNEL_TEXTS = {
  listTitle: 'Все VPN-туннели',
  manageOnly: 'Удалять VPN-туннели и загружать конфиги могут владелец роутера и администратор.',
  deleteHint: 'Удаление необратимо: VPN-туннель исчезнет с роутера вместе с конфигом.',
  toRoutes: 'Перенести правила в «Маршрутах»',
  gone: 'Этого VPN-туннеля нет в снимке роутера — обновите список VPN-туннелей.',
  // Сервер ждёт свежий снимок роутера и отвечает «ещё проверяю»; клиент
  // повторяет запрос около двух минут и сдаётся этими словами.
  checkingTimeout: 'Роутер не отвечает — проверьте, что он на связи. Ничего не удалено.',
}

export function mayManageTunnels(role) {
  return role === 'admin' || role === 'owner'
}

// Только свои VPN-туннели (type managed): удалять сервер разрешает только их,
// остальные отвечают tunnel_not_managed.
export function tunnelList(snapshot) {
  return tunnelRows(snapshot).filter((r) => r.type === 'managed')
}

// Карточка VPN-туннеля. Главный выход -- default_egress снимка (авторитетный
// ответ awg-manager), а не флаг default_route: флаг стоит у всех туннелей
// живого роутера сразу.
export function tunnelCard(snapshot, tunnelID) {
  if (!snapshot || !tunnelID) return null
  const row = tunnelList(snapshot).find((r) => r.id === tunnelID)
  if (!row) return null
  const t = (snapshot.tunnels ?? []).find((x) => x.id === tunnelID) ?? {}
  const egress = String(snapshot.default_egress ?? '').trim()
  return { ...row, iface: t.iface ?? '', egressKnown: egress !== '', isDefault: egress !== '' && egress === row.id }
}

function rulesBlockText(n) {
  const what = n > 0 ? rulesCount(n) : 'есть правила'
  return `На этом VPN-туннеле ${what} — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.`
}

function defaultBlockText(name) {
  return `«${name}» — главный выход роутера: через него идёт всё, что не названо правилами. Удалить его нельзя, пока главным выходом не назначен другой.`
}

// Правила -- первым: их можно убрать переносом прямо из приложения.
export function deleteBlock(card) {
  if (!card) return null
  if ((card.total ?? 0) > 0) return { kind: 'rules', text: rulesBlockText(card.total) }
  if (card.isDefault) return { kind: 'default', text: defaultBlockText(card.name) }
  return null
}

export function deleteSheetText(card) {
  return {
    title: `Удалить VPN-туннель «${card.name}»?`,
    body: `VPN-туннель «${card.name}» исчезнет с роутера вместе с конфигом. Это необратимо: вернуть его можно, только загрузив конфиг заново.`,
    note: 'Перед удалением сервер ещё раз проверит по свежему снимку роутера, что правил на VPN-туннеле нет.',
  }
}

// Число правил из отказа: разбивка {total, dns, static, hr_neo, via_policy}
// (контракт части 1); просто число тоже понимается.
function refusalRulesTotal(rules) {
  const n = Number(rules && typeof rules === 'object' ? rules.total : rules)
  return Number.isFinite(n) && n > 0 ? n : 0
}

// Отказ сервера по снимку -- не ошибка, а те же слова, что до нажатия: к ним
// привязана кнопка переноса, поэтому фраза сервера здесь не подставляется.
export function deleteRefusal(err, card) {
  if (err?.code === 'tunnel_has_rules') return { kind: 'rules', text: rulesBlockText(refusalRulesTotal(err.data?.rules)) }
  if (err?.code === 'tunnel_is_default') return { kind: 'default', text: defaultBlockText(card?.name ?? '') }
  return null
}

const DELETE_ERRORS = {
  confirm_mismatch: 'Имя VPN-туннеля набрано неверно — ничего не удалено.',
  not_found: 'Роутер не знает этот VPN-туннель — обновите список VPN-туннелей.',
  tunnel_not_found: 'Роутер не знает этот VPN-туннель — обновите список VPN-туннелей.',
  tunnel_not_managed: 'Этот VPN-туннель заведён не через awg-manager — из приложения его не удалить.',
  snapshot_partial: 'Роутер прислал неполный снимок маршрутов — проверить правила не вышло, ничего не удалено. Попробуйте через минуту.',
  agent_too_old: 'Удалять VPN-туннели из приложения этот агент не умеет — обновите агента на роутере.',
  // Не код сервера: экран сдался, повторяя запрос на state:"checking".
  checking_timeout: TUNNEL_TEXTS.checkingTimeout,
}

// Сервер присылает готовую русскую фразу -- она первая. Пустая строка --
// своей фразы нет, лист скажет общее «не получилось».
export function deleteErrorText(err) {
  if (err?.serverMessage) return err.serverMessage
  if (err?.code && DELETE_ERRORS[err.code]) return DELETE_ERRORS[err.code]
  if (err?.status === 404) return DELETE_ERRORS.not_found
  if (!err?.status) return 'Сервер не ответил — ничего не удалено. Попробуйте ещё раз.'
  return ''
}

export function deleteOutcome(result, name) {
  return commandOutcome(result, {
    ok: `VPN-туннель «${name}» удалён.`,
    fail: 'Роутер не удалил VPN-туннель',
    pending: 'Команда ушла на роутер, но он пока не ответил. Обновите список VPN-туннелей через минуту.',
  })
}
