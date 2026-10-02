import { describe, it, expect } from 'vitest'
import { backupLine } from '../src/backupLine.js'

// Все времена -- UTC; генерирует ответ сервера в 05:30 7 октября.
const GENERATED = '2026-10-07T05:30:00Z'
const opts = { timeZone: 'UTC', now: Date.parse(GENERATED) }

const small = (over = {}) => ({
  last_ok_at: '2026-10-07T02:00:05Z',
  last_run_at: '2026-10-07T02:00:05Z',
  ok: true,
  size_bytes: 4 * 1024 * 1024,
  telegram: 'ok',
  ...over,
})
const full = (over = {}) => ({
  last_ok_at: '2026-10-07T02:03:00Z',
  last_run_at: '2026-10-07T02:03:00Z',
  ok: true,
  size_bytes: 190 * 1024 * 1024,
  telegram: 'off',
  offsite: 'ok',
  ...over,
})
const verify = (over = {}) => ({ last_run_at: '2026-10-04T03:30:00Z', ok: true, routers: 3, ...over })
const fleet = (backup) => ({ generated_at: GENERATED, backup })
const line = (backup) => backupLine(fleet(backup), opts)

describe('строка «Бэкап» в карточке «Бэкенд»', () => {
  it('нет поля backup (старый бэкенд) -- строки нет', () => {
    expect(backupLine({ generated_at: GENERATED }, opts)).toBeNull()
    expect(backupLine(null, opts)).toBeNull()
  })

  it('всё хорошо: время, размеры, куда ушло, проверка', () => {
    expect(line({ known: true, small: small(), full: full(), verify: verify() })).toEqual({
      text: 'Бэкап: сегодня 02:00 · малый 4 МБ ушёл в Telegram · полный 190 МБ на сервер · проверка восстановления 4 окт',
      tone: 'ok',
    })
  })

  it('проверки восстановления ещё не было -- хвоста нет', () => {
    const l = line({ known: true, small: small(), full: full(), verify: { last_run_at: '', ok: false, routers: 0 } })
    expect(l.text).toBe('Бэкап: сегодня 02:00 · малый 4 МБ ушёл в Telegram · полный 190 МБ на сервер')
    expect(l.tone).toBe('ok')
  })

  it('внешняя цель выключена -- полный на диске Pi; Telegram выключен -- малый тоже', () => {
    const l = line({ known: true, small: small({ telegram: 'off' }), full: full({ offsite: 'off' }), verify: verify() })
    expect(l.text).toBe('Бэкап: сегодня 02:00 · малый 4 МБ на диске Pi · полный 190 МБ на диске Pi · проверка восстановления 4 окт')
  })

  it('вчерашний бэкап в пределах 26 часов -- «вчера»', () => {
    const l = backupLine(
      { generated_at: '2026-10-07T01:00:00Z', backup: { known: true, small: small({ last_ok_at: '2026-10-06T02:00:05Z' }), full: full(), verify: verify() } },
      { timeZone: 'UTC', now: Date.parse('2026-10-07T01:00:00Z') },
    )
    expect(l.text.startsWith('Бэкап: вчера 02:00')).toBe(true)
    expect(l.tone).toBe('ok')
  })

  it('состояние неизвестно', () => {
    expect(line({ known: false })).toEqual({ text: 'Состояние бэкапа неизвестно', tone: 'warn' })
  })

  it('малый старше 26 часов -- «Бэкап не делался N дн»', () => {
    const l = line({
      known: true,
      small: small({ last_ok_at: '2026-10-04T02:00:05Z', last_run_at: '2026-10-04T02:00:05Z' }),
      full: full(),
      verify: verify(),
    })
    expect(l).toEqual({ text: 'Бэкап не делался 3 дн', tone: 'danger' })
    const justOver = line({ known: true, small: small({ last_ok_at: '2026-10-06T02:00:00Z' }), full: full() })
    expect(justOver.text).toBe('Бэкап не делался 1 дн')
    const edge = line({ known: true, small: small({ last_ok_at: '2026-10-06T03:30:01Z' }), full: full() })
    expect(edge.tone).toBe('ok')
  })

  it('малого ещё ни разу не было', () => {
    const never = { last_ok_at: '', last_run_at: '', ok: false, size_bytes: 0, telegram: 'off' }
    expect(line({ known: true, small: never, full: { ...never, offsite: 'off' }, verify: { last_run_at: '', ok: false, routers: 0 } })).toEqual({
      text: 'Бэкап ещё не делался',
      tone: 'warn',
    })
  })

  it('малый не ушёл в Telegram -- с причиной словами', () => {
    const l = line({
      known: true,
      small: small({ ok: false, telegram: 'error', reason: 'архив больше лимита Telegram', last_ok_at: '2026-10-07T02:00:05Z' }),
      full: full(),
      verify: verify(),
    })
    expect(l).toEqual({ text: 'Малый бэкап не ушёл в Telegram: архив больше лимита Telegram', tone: 'danger' })
  })

  it('Telegram не принял после нескольких дней неудач -- добавлено, давно ли был удачный', () => {
    const l = line({
      known: true,
      small: small({ ok: false, telegram: 'error', reason: 'Telegram не принял архив', last_ok_at: '2026-10-03T02:00:05Z' }),
      full: full(),
    })
    expect(l.text).toBe('Малый бэкап не ушёл в Telegram: Telegram не принял архив · последний удачный 4 дн назад')
  })

  it('доставку винит только когда архив есть: размер 0 -- «не сделан» со словами причины', () => {
    const built = line({
      known: true,
      small: small({ ok: false, telegram: 'error', size_bytes: 0, reason: 'не удалось собрать архив' }),
      full: full(),
      verify: verify(),
    })
    expect(built.text).toBe('Малый бэкап не сделан: не удалось собрать архив')
    const fullBuilt = line({
      known: true,
      small: small(),
      full: full({ ok: false, offsite: 'error', size_bytes: 0, reason: 'не удалось собрать архив' }),
      verify: verify(),
    })
    expect(fullBuilt.text).toBe('Полный бэкап не сделан: не удалось собрать архив')
  })

  it('полный ни разу не запускался -- не «на диске Pi», а «ещё не делался»', () => {
    const l = line({ known: true, small: small(), full: { last_ok_at: '', last_run_at: '', ok: false, size_bytes: 0, telegram: 'off', offsite: 'off' }, verify: verify() })
    expect(l.text).toBe('Бэкап: сегодня 02:00 · малый 4 МБ ушёл в Telegram · полный ещё не делался · проверка восстановления 4 окт')
    expect(l.tone).toBe('ok')
  })

  it('полный не сделан / не скопирован на сервер', () => {
    expect(line({ known: true, small: small(), full: full({ ok: false, offsite: 'off', reason: 'архив не записан на диск' }), verify: verify() })).toEqual({
      text: 'Полный бэкап не сделан: архив не записан на диск',
      tone: 'danger',
    })
    expect(line({ known: true, small: small(), full: full({ ok: false, offsite: 'error', reason: 'внешний сервер недоступен' }), verify: verify() })).toEqual({
      text: 'Полный бэкап не скопирован на сервер: внешний сервер недоступен',
      tone: 'danger',
    })
  })

  it('малый не сделан сам по себе (не из-за Telegram)', () => {
    const l = line({ known: true, small: small({ ok: false, telegram: 'off', reason: 'не удалось собрать архив' }), full: full(), verify: verify() })
    expect(l.text).toBe('Малый бэкап не сделан: не удалось собрать архив')
  })

  it('проверка восстановления не прошла', () => {
    const l = line({ known: true, small: small(), full: full(), verify: verify({ ok: false, reason: 'числа в архиве не сходятся с записанными при бэкапе' }) })
    expect(l).toEqual({
      text: 'Проверка восстановления не прошла: числа в архиве не сходятся с записанными при бэкапе',
      tone: 'danger',
    })
  })

  it('несколько бед -- через « · », все названы', () => {
    const l = line({
      known: true,
      small: small({ ok: false, telegram: 'error', reason: 'Telegram не принял архив' }),
      full: full({ ok: false, offsite: 'off', reason: 'архив не записан на диск' }),
      verify: verify({ ok: false, reason: 'архив не открывается' }),
    })
    expect(l.text).toBe(
      'Малый бэкап не ушёл в Telegram: Telegram не принял архив · Полный бэкап не сделан: архив не записан на диск · Проверка восстановления не прошла: архив не открывается',
    )
    expect(l.tone).toBe('danger')
  })

  it('прогон идёт (начат недавно, не завершён) -- не тревога', () => {
    const l = line({
      known: true,
      small: small(),
      full: full({ ok: false, unfinished: true, reason: 'прогон не завершён', last_run_at: '2026-10-07T05:00:00Z', offsite: 'error' }),
      verify: verify(),
    })
    expect(l).toEqual({ text: 'Бэкап: сегодня 02:00 · малый 4 МБ ушёл в Telegram · полный бэкап сейчас делается · проверка восстановления 4 окт', tone: 'ok' })
  })

  it('прогон начат больше двух часов назад -- это провал', () => {
    const l = line({
      known: true,
      small: small(),
      full: full({ ok: false, unfinished: true, reason: 'прогон не завершён', last_run_at: '2026-10-07T02:03:00Z', offsite: 'error' }),
      verify: verify(),
    })
    expect(l.text).toBe('Полный бэкап не сделан: прогон не завершён')
    expect(l.tone).toBe('danger')
  })

  it('без причины от сервера -- без двоеточия, не «undefined»', () => {
    const l = line({ known: true, small: small({ ok: false, telegram: 'error' }), full: full(), verify: verify() })
    expect(l.text).toBe('Малый бэкап не ушёл в Telegram')
    expect(JSON.stringify(l)).not.toMatch(/undefined|null|NaN/)
  })

  it('сырые строки не показываются: латиница вне «ёлочек» и слов-исключений не пролезает из причины', () => {
    // Строку причины пишет сервер из закрытого набора фраз; сама функция её не фильтрует,
    // но всё остальное в тексте -- только русские слова и единицы.
    const l = line({ known: true, small: small(), full: full(), verify: verify() })
    expect(l.text.replace(/Telegram|Pi/g, '')).not.toMatch(/[A-Za-z]/)
  })
})
