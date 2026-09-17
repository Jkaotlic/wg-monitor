// Загрузка своего .conf: чистые функции экрана ConfImportScreen.jsx.
//
// В файле приватный ключ. Поэтому всё, что можно проверить без содержимого,
// проверяется до чтения (расширение, размер), а содержимое экран держит только
// в ref и отдаёт серверу один раз. Сервер хранит конфиг под одноразовым
// токеном предпросмотра и обратно его не отдаёт.
import { commandOutcome } from './commandWait.js'

export const CONF_MAX_BYTES = 50 * 1024

// Опрос предпросмотра: токен живёт на сервере 5 минут, опрос кончается
// раньше -- иначе последний ответ был бы «проверка устарела».
export const IMPORT_POLL_DEADLINE_MS = 4 * 60_000

// Правило имени -- как у бота и сервера (^[a-z][a-z0-9_-]{1,31}$): роутер
// берёт имя в идентификаторы, и всё лишнее он отвергнет.
export const TUNNEL_NAME_RE = /^[a-z][a-z0-9_-]{1,31}$/
const NAME_RULE = 'латиница в нижнем регистре, цифры, «-» и «_»; от 2 до 32 знаков, первая — буква.'

export const IMPORT_TEXTS = {
  title: 'Загрузить конфиг .conf',
  navNote: 'WireGuard · AmneziaWG',
  intro: 'Конфиг WireGuard или AmneziaWG встанет на роутер новым VPN-туннелем рядом с остальными. Работающие VPN-туннели и правила он не трогает.',
  pick: 'Выбрать файл .conf',
  pickAnother: 'Выбрать другой файл',
  privacy: 'В файле приватный ключ. Приложение отправит его на сервер один раз — для проверки и добавления — и нигде у себя не сохранит. Если проверка не удалась, файл нужно выбрать заново.',
  nameHint: `Имя VPN-туннеля на роутере: ${NAME_RULE}`,
  readFailed: 'Файл не прочитался — выберите его ещё раз.',
  notAnalyzed: 'Проверить конфиг заранее этот роутер не умеет: агент на нём старше v0.28. Роутер проверит конфиг сам, когда будет добавлять VPN-туннель.',
  analyzing: 'Проверка ещё идёт: роутер проверяет конфиг…',
  analyzeSlow: 'Проверка ещё идёт: роутер пока не закончил её. Если он спит, проверка закончится, когда он проснётся.',
  pickAgain: 'Выберите файл заново.',
  analyzedClean: 'Роутер проверил конфиг: замечаний нет.',
  blocking: 'Роутер не примет этот конфиг — исправьте ошибки в файле и выберите его заново.',
  replaceHint:
    'Это добавление, а не замена: работающий VPN-туннель останется как есть. Чтобы заменить его этим конфигом, добавьте новый, перенесите на него правила в «Маршрутах» и только потом удалите прежний — до последнего шага всё обратимо. Или воспользуйтесь мастером «Заменить конфиг VPN-туннеля»: он выпускает конфиг в кабинете сам.',
  running: 'Добавляем VPN-туннель на роутер — это может занять до минуты…',
}

export function confFileProblem(file) {
  if (!file) return 'Файл не выбран.'
  if (!/\.conf$/i.test(String(file.name ?? ''))) return 'Нужен файл с расширением .conf — конфиг WireGuard или AmneziaWG.'
  if (!(file.size > 0)) return 'Файл пустой.'
  if (file.size > CONF_MAX_BYTES) return 'Файл больше 50 КиБ — это не конфиг VPN-туннеля.'
  return ''
}

// Подсказка имени из имени файла -- тем же преобразованием, что у бота
// (sanitizeTunnelName), плюс «vpn-» перед цифрой: имя обязано начинаться с буквы.
export function suggestTunnelName(fileName) {
  let s = String(fileName ?? '')
    .replace(/\.conf$/i, '')
    .toLowerCase()
    .replace(/[^a-z0-9_]/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-+|-+$/g, '')
  if (s && !/^[a-z]/.test(s)) s = `vpn-${s}`
  s = s.slice(0, 32).replace(/-+$/, '')
  return TUNNEL_NAME_RE.test(s) ? s : ''
}

export function tunnelNameProblem(name, snapshot) {
  const v = String(name ?? '').trim()
  if (!v) return 'Введите имя VPN-туннеля.'
  if (!TUNNEL_NAME_RE.test(v)) return `Имя не подходит: ${NAME_RULE}`
  const taken = (snapshot?.tunnels ?? []).some(
    (t) => String(t.name ?? '').trim().toLowerCase() === v || String(t.id ?? '').trim().toLowerCase() === v,
  )
  if (taken) return `VPN-туннель «${v}» уже есть на роутере — выберите другое имя.`
  return ''
}

// Кусками: String.fromCharCode(...все байты) упирается в предел аргументов.
export function bytesToBase64(bytes) {
  let bin = ''
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(bin)
}

// Байты, а не текст: конфиг уходит серверу ровно таким, каким лежит в файле.
export function readConfBase64(file, Reader = globalThis.FileReader) {
  return new Promise((resolve, reject) => {
    const reader = new Reader()
    reader.onload = () => resolve(bytesToBase64(new Uint8Array(reader.result)))
    reader.onerror = () => reject(new Error('read_failed'))
    reader.readAsArrayBuffer(file)
  })
}

function listText(value) {
  const items = Array.isArray(value) ? value : typeof value === 'string' ? value.split(',') : []
  const clean = items.map((x) => String(x ?? '').trim()).filter(Boolean)
  return clean.length ? clean.join(', ') : 'не указаны'
}

// Замечание анализа: объект {severity, code?, message} (контракт части 1);
// строка и level тоже понимаются. Ошибка -- то, с чем модуль роутера конфиг
// не примет; остальное -- замечание.
function problemRow(item) {
  if (typeof item === 'string') return item.trim() ? { tone: 'warn', text: item.trim() } : null
  if (!item || typeof item !== 'object') return null
  const text = String(item.message ?? item.text ?? item.code ?? '').trim()
  if (!text) return null
  const level = String(item.severity ?? item.level ?? '').toLowerCase()
  return { tone: level === 'error' ? 'error' : 'warn', text }
}

// analyzing -- роутер ещё не ответил на проверку, экран опрашивает дальше.
// canConfirm -- готовое решение сервера (can_confirm: state ready и нет
// ошибок); без поля -- то же правило на клиенте.
export function previewView(resp) {
  const p = resp?.preview ?? {}
  const problems = (Array.isArray(p.problems) ? p.problems : []).map(problemRow).filter(Boolean)
  const blocking = problems.some((x) => x.tone === 'error')
  const analyzing = resp?.state === 'analyzing'
  const canConfirm = !resp ? false : typeof resp.can_confirm === 'boolean' ? resp.can_confirm && !analyzing : !analyzing && !blocking
  return {
    endpoint: String(p.endpoint ?? '').trim() || 'не указан',
    addresses: listText(p.addresses),
    dns: listText(p.dns),
    mtu: p.mtu ? String(p.mtu) : 'по умолчанию',
    problems,
    analyzed: resp?.analyzed === true,
    analyzing,
    note: typeof resp?.note === 'string' ? resp.note.trim() : '',
    blocking,
    canConfirm,
  }
}

const IMPORT_ERRORS = {
  invalid_conf: 'Это не похоже на конфиг WireGuard или AmneziaWG — проверьте файл и выберите его заново.',
  invalid_name: `Имя не подходит: ${NAME_RULE}`,
  name_taken: 'VPN-туннель с таким именем уже есть на роутере — выберите другое имя.',
  conf_too_large: 'Файл больше 50 КиБ — это не конфиг VPN-туннеля.',
  preview_expired: 'Проверка устарела: с неё прошло больше 5 минут. Выберите файл заново.',
  preview_not_ready: 'Роутер ещё проверяет конфиг — подождите немного.',
  analysis_pending: 'Проверка ещё идёт — роутер пока не закончил проверять конфиг.',
  conf_rejected: 'Роутер не примет этот конфиг — исправьте ошибки в файле и выберите его заново.',
  agent_too_old: 'Загружать конфиги из приложения этот агент не умеет — обновите агента на роутере.',
}

export function importErrorText(err) {
  if (err?.serverMessage) return err.serverMessage
  if (err?.code && IMPORT_ERRORS[err.code]) return IMPORT_ERRORS[err.code]
  if (!err?.status) return 'Сервер не ответил — попробуйте ещё раз.'
  return 'Не получилось. Попробуйте ещё раз.'
}

// После отказа проверки конфиг уже стёрт с клиента: файл выбирается заново,
// и экран говорит это, если фраза отказа ещё не сказала.
export function withPickAgain(text) {
  const t = String(text ?? '').trim()
  if (!t) return IMPORT_TEXTS.pickAgain
  return /заново/i.test(t) ? t : `${t} ${IMPORT_TEXTS.pickAgain}`
}

export function importOutcome(result, name) {
  return commandOutcome(result, {
    ok: `VPN-туннель «${name}» добавлен. Перенести на него правила можно в «Маршрутах».`,
    fail: 'Роутер не добавил VPN-туннель',
    pending: 'Команда ушла на роутер, но он пока не ответил. Загляните в список VPN-туннелей через минуту.',
  })
}
