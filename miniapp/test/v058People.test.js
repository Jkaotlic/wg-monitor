import { describe, it, expect } from 'vitest'
import { personTitle, personSub, seenText, matchesQuery, pickList, peopleByID } from '../src/people.js'

// Имена вымышленные.
const NOW = Date.parse('2026-10-08T12:00:00Z')
const TZ = 'UTC'
const opts = { now: NOW, timeZone: TZ }

const ivan = { telegram_user_id: 1001, name: 'Иван Петров', username: 'ivan_p', last_seen_at: '2026-10-08T09:00:00Z', is_admin: false, routers: [{ id: 7, nickname: 'дача-север', role: 'owner' }] }
const olga = { telegram_user_id: 1002, name: 'Ольга', username: '', last_seen_at: '2026-10-08T11:55:00Z', is_admin: false, routers: [] }
const nick = { telegram_user_id: 1003, name: '', username: 'rook42', last_seen_at: null, is_admin: false, routers: [{ id: 9, nickname: 'офис', role: 'operator' }] }
const bare = { telegram_user_id: 1004, name: '', username: '', last_seen_at: null, is_admin: false, routers: [] }

describe('people: заголовок человека', () => {
  it('имя и @ник', () => expect(personTitle(ivan)).toBe('Иван Петров (@ivan_p)'))
  it('только имя', () => expect(personTitle(olga)).toBe('Ольга'))
  it('только ник', () => expect(personTitle(nick)).toBe('@rook42'))
  it('ни имени, ни ника -- номер', () => expect(personTitle(bare)).toBe('номер 1004'))
  it('пробелы по краям не считаются именем', () => expect(personTitle({ ...bare, name: '  ', username: ' ' })).toBe('номер 1004'))
})

describe('people: когда видели', () => {
  it('нет даты -- пусто', () => expect(seenText(null, opts)).toBe(''))
  it('битая дата -- пусто', () => expect(seenText('вчера', opts)).toBe(''))
  it('меньше минуты -- только что', () => expect(seenText('2026-10-08T11:59:40Z', opts)).toBe('только что'))
  it('меньше часа -- минуты назад', () => expect(seenText('2026-10-08T11:55:00Z', opts)).toBe('5 мин назад'))
  it('раньше сегодня -- сегодня', () => expect(seenText('2026-10-08T09:00:00Z', opts)).toBe('сегодня'))
  it('вчера', () => expect(seenText('2026-10-07T20:00:00Z', opts)).toBe('вчера'))
  it('давно -- дни назад', () => expect(seenText('2026-10-03T12:00:00Z', opts)).toBe('5 дн назад'))
  it('часы будущего (рассинхрон) -- только что', () => expect(seenText('2026-10-08T12:03:00Z', opts)).toBe('только что'))
})

describe('people: подпись под именем', () => {
  it('владелец роутера и когда был', () => expect(personSub(ivan, opts)).toBe('владелец дача-север · был сегодня · номер 1001'))
  it('без доступа -- ждёт доступа, писал боту', () => expect(personSub(olga, opts)).toBe('ждёт доступа · писал боту 5 мин назад · номер 1002'))
  it('не видели -- без времени', () => expect(personSub(nick, opts)).toBe('оператор офис · номер 1003'))
  it('номер уже в заголовке -- не повторяется', () => expect(personSub(bare, opts)).toBe('ждёт доступа'))
  it('админ без роутеров -- не «ждёт доступа»', () => {
    expect(personSub({ ...bare, is_admin: true }, opts)).toBe('администратор')
    expect(personSub({ ...olga, is_admin: true }, opts)).toBe('администратор · был 5 мин назад · номер 1002')
  })
  it('много роутеров -- два и «ещё N»', () => {
    const many = { ...bare, routers: [
      { id: 1, nickname: 'a', role: 'owner' },
      { id: 2, nickname: 'b', role: 'operator' },
      { id: 3, nickname: 'c', role: 'operator' },
      { id: 4, nickname: 'd', role: 'owner' },
    ] }
    expect(personSub(many, opts)).toBe('владелец a, оператор b и ещё 2')
  })
})

describe('people: поиск', () => {
  it('пустой запрос -- все', () => expect([ivan, olga, nick, bare].filter((p) => matchesQuery(p, '  '))).toHaveLength(4))
  it('по имени без учёта регистра', () => expect(matchesQuery(ivan, 'петр')).toBe(true))
  it('по нику, с @ и без', () => {
    expect(matchesQuery(nick, '@rook')).toBe(true)
    expect(matchesQuery(nick, 'ROOK4')).toBe(true)
  })
  it('по номеру', () => expect(matchesQuery(bare, '100')).toBe(true))
  it('по имени роутера', () => expect(matchesQuery(ivan, 'дача')).toBe(true))
  it('чужое не находится', () => {
    expect(matchesQuery(olga, 'дача')).toBe(false)
    expect(matchesQuery(ivan, '@ольга')).toBe(false)
  })
  it('«@» в одиночку -- все', () => expect(matchesQuery(bare, '@')).toBe(true))
  it('ё и е -- одна буква: в запросе и в данных', () => {
    const petr = { ...bare, name: 'Пётр Сидоров' }
    expect(matchesQuery(petr, 'петр')).toBe(true)
    expect(matchesQuery(petr, 'ПЁТР')).toBe(true)
    expect(matchesQuery({ ...bare, name: 'Петр' }, 'пётр')).toBe(true)
    const yolka = { ...bare, routers: [{ id: 3, nickname: 'Ёлки-дача', role: 'owner' }] }
    expect(matchesQuery(yolka, 'елки')).toBe(true)
  })
})

describe('people: список для выбора', () => {
  const access = { owner: { telegram_user_id: 1001 }, operators: [{ telegram_user_id: 1003 }] }
  const all = [ivan, olga, nick, bare]

  it('оператору: владелец и операторы ЭТОГО роутера отмечены и в конце', () => {
    const list = pickList(all, { access, role: 'operator' })
    expect(list.map((r) => r.id)).toEqual([1002, 1004, 1001, 1003])
    expect(list.map((r) => r.taken)).toEqual([null, null, 'уже владелец', 'уже оператор'])
  })
  it('владельцу: отмечен только текущий владелец', () => {
    const list = pickList(all, { access, role: 'owner' })
    expect(list.filter((r) => r.taken).map((r) => r.id)).toEqual([1001])
    expect(list.find((r) => r.id === 1003).taken).toBe(null)
  })
  it('порядок сервера среди выбираемых сохраняется', () => {
    const list = pickList([bare, olga], { access: { owner: null, operators: [] }, role: 'operator' })
    expect(list.map((r) => r.id)).toEqual([1004, 1002])
  })
  it('поиск сужает список', () => {
    expect(pickList(all, { access, role: 'operator', query: 'ольг' }).map((r) => r.id)).toEqual([1002])
  })
  it('у строки готовы подписи', () => {
    const [row] = pickList([olga], { access: null, role: 'operator', ...opts })
    expect(row).toEqual({ id: 1002, title: 'Ольга', sub: 'ждёт доступа · писал боту 5 мин назад · номер 1002', taken: null })
  })
  it('без людей и мусор -- пустой список', () => {
    expect(pickList(null, { access, role: 'operator' })).toEqual([])
    expect(pickList([{ name: 'без номера' }, ivan], { access: null, role: 'operator' }).map((r) => r.id)).toEqual([1001])
  })
})

describe('people: справочник по номеру', () => {
  it('номер -> человек', () => {
    const m = peopleByID([ivan, olga])
    expect(m.get(1001)).toBe(ivan)
    expect(m.get(5)).toBe(undefined)
  })
  it('нет справочника -- пустой', () => expect(peopleByID(null).size).toBe(0))
})
