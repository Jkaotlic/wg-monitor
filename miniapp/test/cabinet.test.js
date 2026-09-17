import { describe, it, expect } from 'vitest'
import { accountSummary, optionRows } from '../src/cabinet.js'

describe('accountSummary', () => {
  it('подключённый кабинет говорит про подписку и устройства', () => {
    const s = accountSummary({
      provider: 'amnezia', label: 'Amnezia Premium', connected: true,
      status: 'active', ends_at: '2026-12-01', devices_used: 2, devices_max: 5,
      options: [{ id: 'nl', label: 'Нидерланды' }],
    })
    expect(s.title).toBe('Amnezia Premium')
    expect(s.lines).toContain('Устройств занято 2 из 5')
    expect(s.lines.join(' ')).toContain('2026-12-01')
    expect(s.canIssue).toBe(true)
  })

  // Свободных мест нет -- выпускать нельзя, и сказать это надо ДО того, как
  // человек выберет страну и получит отказ от кабинета.
  it('без свободных устройств выпуск закрыт и объяснён', () => {
    const s = accountSummary({
      provider: 'amnezia', connected: true, devices_used: 5, devices_max: 5,
      options: [{ id: 'nl', label: 'Нидерланды' }],
    })
    expect(s.canIssue).toBe(false)
    expect(s.full).toBe(true)
    expect(s.reason).toContain('мест')
  })

  // Сервер (amneziaSlotBusy) выпущенную страну пускает и при полной подписке:
  // она уже занимает своё место. Гасить её нельзя.
  it('полная подписка: выпущенная страна доступна, новые -- нет', () => {
    const acc = {
      provider: 'amnezia', connected: true, devices_used: 3, devices_max: 3,
      options: [{ id: 'nl', label: 'Нидерланды', issued: true }, { id: 'de', label: 'Германия' }],
    }
    const s = accountSummary(acc)
    expect(s.canIssue).toBe(true)
    expect(s.full).toBe(true)
    expect(s.reason).toContain('выпуск новых стран закрыт')
    expect(s.fullNote).toBe('Свободных мест в подписке нет — выпуск новых стран закрыт.')
    expect(optionRows(acc).map((o) => [o.id, o.available])).toEqual([['nl', true], ['de', false]])
    expect(optionRows({ ...acc, devices_used: 1 }).map((o) => o.available)).toEqual([true, true])
  })

  // Неподключённый кабинет -- состояние, а не поломка: у него своя фраза, и
  // приложение не спрашивает ключей.
  it('неподключённый кабинет объясняет себя словами кабинета', () => {
    const s = accountSummary({ provider: 'hidemyname', label: 'HideMy.name', connected: false, note: 'Код доступа не сохранён.' })
    expect(s.canIssue).toBe(false)
    expect(s.reason).toBe('Код доступа не сохранён.')
  })
})

describe('optionRows', () => {
  it('уже выпущенное помечено, чтобы не выпускать второй раз вслепую', () => {
    const rows = optionRows({ options: [{ id: 'nl', label: 'Нидерланды' }, { id: 'de', label: 'Германия', issued: true }] })
    expect(rows[0]).toEqual({ id: 'nl', label: 'Нидерланды', note: '', issued: false, available: true })
    expect(rows[1]).toEqual({ id: 'de', label: 'Германия', note: 'уже выпущен', issued: true, available: true })
  })

  it('пустой список остаётся пустым, а не выдумывает строки', () => {
    expect(optionRows(null)).toEqual([])
  })
})

// Отзыв теперь живёт в кабинете приложения, и его видят админ и владелец.
// Им текст говорит «отзовите ниже»; остальным -- кто это может, а не «в боте».
describe('кончились места', () => {
  const full = {
    connected: true,
    label: 'Amnezia Premium',
    devices_max: 3,
    devices_used: 3,
    options: [{ id: 'nl', label: 'Нидерланды', issued: true }],
  }

  it('может отозвать -- «отзовите ниже»', () => {
    const s = accountSummary(full, { canRevoke: true })
    expect(s.canIssue).toBe(true)
    expect(s.reason).toBe('Свободных мест в подписке нет — выпуск новых стран закрыт. Уже выпущенные можно выпустить заново. Освободите место — отзовите одну из выпущенных стран ниже.')
  })

  it('не может -- кто может; без второго аргумента так же', () => {
    const text = 'Свободных мест в подписке нет — выпуск новых стран закрыт. Уже выпущенные можно выпустить заново. Освободить место может владелец роутера или администратор: для этого отзывается одна из выпущенных стран.'
    expect(accountSummary(full, { canRevoke: false }).reason).toBe(text)
    expect(accountSummary(full).reason).toBe(text)
  })

  it('ни одного текста про бота', () => {
    for (const opts of [{ canRevoke: true }, { canRevoke: false }]) {
      expect(accountSummary(full, opts).reason).not.toMatch(/бот/i)
      expect(accountSummary(full, opts).reason).toMatch(/освобод/i)
    }
  })
})
