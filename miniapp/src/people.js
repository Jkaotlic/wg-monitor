import { agoText, whenText } from './when.js'
import { humanAge } from './labels.js'

// Справочник людей (v0.58, GET /v1/miniapp/people): кого админ выбирает в
// доступ по имени, а не по номеру Telegram. Имена и ники -- данные людей, не
// разметка: здесь только строки, рисует их экран текстом.

const ROLE = { owner: 'владелец', operator: 'оператор' }
const TAKEN = { owner: 'уже владелец', operator: 'уже оператор' }
const SHOWN_ROUTERS = 2

const clean = (s) => (typeof s === 'string' ? s.trim() : '')
const idOf = (p) => (Number.isInteger(p?.telegram_user_id) && p.telegram_user_id > 0 ? p.telegram_user_id : 0)

function hasLabel(p) {
  return Boolean(clean(p?.name) || clean(p?.username))
}

// «Иван Петров (@ivan)»; без имени -- «@ivan»; без того и другого -- номер.
export function personTitle(p) {
  const name = clean(p?.name)
  const user = clean(p?.username)
  if (name && user) return `${name} (@${user})`
  if (name) return name
  if (user) return `@${user}`
  return `номер ${idOf(p)}`
}

// Давность без часов и минут: человеку в выборе важно «живой ли», а не когда
// точно. «только что», «5 мин назад», «сегодня», «вчера», «5 дн назад».
export function seenText(iso, { now = Date.now(), timeZone } = {}) {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const sec = Math.max(0, Math.floor((now - t) / 1000))
  if (sec < 3600) return agoText(sec)
  const when = whenText(iso, { now, timeZone })
  if (when.startsWith('сегодня')) return 'сегодня'
  if (when.startsWith('вчера')) return 'вчера'
  return `${humanAge(sec)} назад`
}

function rolesText(p) {
  const routers = Array.isArray(p?.routers) ? p.routers.filter((r) => ROLE[r?.role]) : []
  const parts = []
  if (p?.is_admin) parts.push('администратор')
  if (routers.length) {
    const shown = routers.slice(0, SHOWN_ROUTERS).map((r) => `${ROLE[r.role]} ${clean(r.nickname) || `роутера ${r.id}`}`)
    const rest = routers.length - shown.length
    parts.push(shown.join(', ') + (rest > 0 ? ` и ещё ${rest}` : ''))
  }
  return parts.join(', ')
}

// Строка под именем: «владелец router-a · был сегодня · номер 123» или
// «ждёт доступа · писал боту 5 мин назад · номер 123». Номер -- если его нет
// в заголовке.
export function personSub(p, opts = {}) {
  const roles = rolesText(p)
  const seen = seenText(p?.last_seen_at, opts)
  const parts = [roles || 'ждёт доступа']
  if (seen) parts.push(`${roles ? 'был' : 'писал боту'} ${seen}`)
  if (hasLabel(p)) parts.push(`номер ${idOf(p)}`)
  return parts.join(' · ')
}

// Для поиска ё и е -- одна буква: «петр» находит «Пётр», и наоборот.
const fold = (s) => clean(s).toLowerCase().replace(/ё/g, 'е')

// Поиск по имени, @нику, номеру и имени роутера. Пустой запрос -- все.
export function matchesQuery(p, query) {
  const q = fold(query).replace(/^@/, '')
  if (!q) return true
  const hay = [p?.name, p?.username, String(idOf(p)), ...(Array.isArray(p?.routers) ? p.routers.map((r) => r?.nickname) : [])]
  return hay.some((s) => fold(s).includes(q))
}

// Кто уже имеет на ЭТОМ роутере ту роль, которую выдаёт форма: оператору
// -- владелец и операторы, владельцу -- текущий владелец. Берётся из ответа
// /access (он свежее справочника: только что добавленный уже там).
function takenOn(access, role) {
  const taken = new Map()
  const owner = idOf(access?.owner)
  if (owner) taken.set(owner, TAKEN.owner)
  if (role === 'operator') {
    for (const op of access?.operators ?? []) {
      const id = idOf(op)
      if (id && !taken.has(id)) taken.set(id, TAKEN.operator)
    }
  }
  return taken
}

// Строки выбора: порядок сервера (новые, ждущие доступа, -- первыми), уже
// имеющие роль на этом роутере -- в конце, отмеченные и невыбираемые.
export function pickList(people, { access = null, role = 'operator', query = '', now, timeZone } = {}) {
  if (!Array.isArray(people)) return []
  const taken = takenOn(access, role)
  const rows = people
    .filter((p) => idOf(p) && matchesQuery(p, query))
    .map((p) => ({ id: idOf(p), title: personTitle(p), sub: personSub(p, { now, timeZone }), taken: taken.get(idOf(p)) ?? null }))
  return [...rows.filter((r) => !r.taken), ...rows.filter((r) => r.taken)]
}

export function peopleByID(people) {
  const m = new Map()
  for (const p of Array.isArray(people) ? people : []) if (idOf(p)) m.set(idOf(p), p)
  return m
}
