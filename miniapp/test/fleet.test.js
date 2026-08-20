import { describe, it, expect } from 'vitest'
import { sortByUrgency, fleetRow, batchProgress } from '../src/fleet.js'

const r = (id, status, nickname = 'r' + id) => ({ id, status, nickname })

describe('sortByUrgency', () => {
  it('тревога впереди офлайна, офлайн впереди спящего, спящий впереди живого', () => {
    const out = sortByUrgency([r(1, 'online'), r(2, 'sleeping'), r(3, 'alert'), r(4, 'offline')])
    expect(out.map((x) => x.id)).toEqual([3, 4, 2, 1])
  })

  it('внутри одного статуса -- по имени, чтобы порядок не прыгал между обновлениями', () => {
    const out = sortByUrgency([r(1, 'online', 'Дача'), r(2, 'online', 'Ватикан'), r(3, 'online', 'Дом')])
    expect(out.map((x) => x.nickname)).toEqual(['Ватикан', 'Дача', 'Дом'])
  })

  it('неизвестный статус уходит в конец, а не роняет сортировку', () => {
    const out = sortByUrgency([r(1, 'нечто'), r(2, 'alert')])
    expect(out.map((x) => x.id)).toEqual([2, 1])
  })

  it('не мутирует входной массив', () => {
    const input = [r(1, 'online'), r(2, 'alert')]
    sortByUrgency(input)
    expect(input.map((x) => x.id)).toEqual([1, 2])
  })

  it('пустой список остаётся пустым', () => {
    expect(sortByUrgency([])).toEqual([])
    expect(sortByUrgency()).toEqual([])
  })
})

// Строка флота: имя, пилюля состояния, фраза о том, что не так, и пять точек
// -- те же пять служб, что нарисованы лампами на экране роутера.
describe('fleetRow', () => {
  it('живой роутер без тревог говорит, когда отчитался', () => {
    const row = fleetRow({ id: 1, nickname: 'Дом', status: 'online', last_seen_age_sec: 42 })
    expect(row.pill).toEqual({ tone: 'ok', text: 'в порядке' })
    expect(row.sub).toBe('отчёт 42 сек назад')
  })

  // На строке флота важно не «сколько тревог», а «что именно сломалось»:
  // число человеку ничего не говорит, а фраза говорит.
  it('тревога называется словами, а не числом', () => {
    const row = fleetRow({
      id: 1, nickname: 'Дом', status: 'alert', last_seen_age_sec: 10,
      active_incidents: [{ check_name: 'hydraroute', fail_count: 3 }],
    })
    expect(row.pill.tone).toBe('danger')
    expect(row.sub).toBe('Не работает обход блокировок')
  })

  it('молчащий роутер называет, сколько молчит', () => {
    const row = fleetRow({ id: 1, nickname: 'Офис', status: 'offline', last_seen_age_sec: 7200 })
    expect(row.pill).toEqual({ tone: 'danger', text: 'нет ответа 2 ч' })
  })

  it('роутер, не отвечавший ни разу, не выдумывает возраст', () => {
    const row = fleetRow({ id: 1, nickname: 'Новый', status: 'offline' })
    expect(row.pill.text).toBe('ни разу не отвечал')
    expect(row.sub).toBe('агент установлен, но отчётов от него не было')
  })

  it('пять точек идут в порядке ламп, а не в порядке ответа', () => {
    const row = fleetRow({
      id: 1, nickname: 'Дом', status: 'alert', last_seen_age_sec: 10,
      checks: [
        { check_name: 'tunnels', status: 'ok' },
        { check_name: 'dns', status: 'ok' },
        { check_name: 'hydraroute', status: 'fail' },
      ],
    })
    expect(row.dots.map((d) => d.key)).toEqual(['dns', 'external_reach', 'hydraroute', 'awg_manager', 'tunnels'])
    expect(row.dots.map((d) => d.tone)).toEqual(['ok', 'off', 'danger', 'off', 'ok'])
  })

  // Серая точка -- «роутер не сказал», и это не то же самое, что «сломано».
  it('без ответа про службу точка серая, а не красная', () => {
    const row = fleetRow({ id: 1, nickname: 'Дом', status: 'online', last_seen_age_sec: 5 })
    expect(row.dots.every((d) => d.tone === 'off')).toBe(true)
  })
})

// Групповой опрос: одна кнопка, N роутеров, и человек обязан видеть, чем
// кончилось у каждого. «Готово» на группе, где двое не ответили, -- вранье.
describe('batchProgress', () => {
  it('пока идёт — говорит, сколько ответили', () => {
    expect(batchProgress({ total: 3, ok: 1, failed: 0, done: 1 })).toBe('Ответил 1 из 3…')
  })

  it('все ответили — говорит об этом прямо', () => {
    expect(batchProgress({ total: 3, ok: 3, failed: 0, done: 3 })).toBe('Опрошены все 3')
  })

  it('часть не ответила — называет и тех, и других', () => {
    expect(batchProgress({ total: 3, ok: 2, failed: 1, done: 3 })).toBe('Ответили 2 из 3, не ответил 1')
  })

  it('до запуска — пусто, а не «0 из 0»', () => {
    expect(batchProgress(null)).toBe('')
  })
})
