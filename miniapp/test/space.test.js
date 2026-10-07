import { describe, it, expect } from 'vitest'
import {
  SPACE_MIN_VERSION,
  SPACE_TEXTS,
  spaceAvailable,
  parseSpaceReport,
  spaceSummaryRows,
  spaceTopRows,
  cleanOutcomeText,
  spaceFailureText,
} from '../src/space.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'

const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })
const REPORT = { free_kb: 204800, total_kb: 1048576, top: [{ path: '/opt/var/log', kb: 51200 }, { path: '/opt/lib/opkg', kb: 900 }] }

describe('space: кому экран', () => {
  it('отчёт о месте -- админ и агент от v0.57.0', () => {
    expect(SPACE_MIN_VERSION).toBe('v0.57.0')
    expect(spaceAvailable({ role: 'admin', agent_version: 'v0.57.0' })).toBe(true)
    expect(spaceAvailable({ role: 'admin', agent_version: 'v0.56.3' })).toBe(false)
    expect(spaceAvailable({ role: 'operator', agent_version: 'v0.57.0' })).toBe(false)
    expect(spaceAvailable(null)).toBe(false)
  })
})

describe('parseSpaceReport', () => {
  it('разбирает ответ агента', () => {
    expect(parseSpaceReport(ok(REPORT))).toEqual({ freeKB: 204800, totalKB: 1048576, top: [{ path: '/opt/var/log', kb: 51200 }, { path: '/opt/lib/opkg', kb: 900 }] })
  })
  it('top пуст или мусор -- пустой список, не падение', () => {
    expect(parseSpaceReport(ok({ free_kb: 1, total_kb: 2, top: null })).top).toEqual([])
    expect(parseSpaceReport(ok({ free_kb: 1, total_kb: 2, top: [{ path: 3 }, { path: '/opt/x', kb: 'a' }] })).top).toEqual([])
  })
  it('не ok, не JSON, без чисел -- null', () => {
    expect(parseSpaceReport({ status: 'err', output: JSON.stringify(REPORT) })).toBeNull()
    expect(parseSpaceReport({ status: 'ok', output: 'df: /opt: not found' })).toBeNull()
    expect(parseSpaceReport(ok({ top: [] }))).toBeNull()
    expect(parseSpaceReport(null)).toBeNull()
  })
})

describe('spaceSummaryRows', () => {
  it('свободно из всего, с долей', () => {
    const [row] = spaceSummaryRows(parseSpaceReport(ok(REPORT)))
    expect(row).toEqual({ key: 'free', title: 'Свободно на накопителе', value: '200 МБ из 1,0 ГБ (20%)' })
  })
  it('меньше десятой части -- предупреждение', () => {
    const [row] = spaceSummaryRows(parseSpaceReport(ok({ ...REPORT, free_kb: 51200 })))
    expect(row.tone).toBe('warn')
  })
  it('места нет совсем -- так и сказано', () => {
    const [row] = spaceSummaryRows(parseSpaceReport(ok({ ...REPORT, free_kb: 0 })))
    expect(row).toMatchObject({ value: 'нет свободного места', tone: 'danger' })
  })
})

describe('spaceTopRows', () => {
  it('каталог -- размер', () => {
    expect(spaceTopRows(parseSpaceReport(ok(REPORT)))).toEqual([
      { key: '/opt/var/log', title: '/opt/var/log', value: '50 МБ' },
      { key: '/opt/lib/opkg', title: '/opt/lib/opkg', value: '900 КБ' },
    ])
  })
  it('пусто -- пустой список, а экран говорит словами', () => {
    expect(spaceTopRows(parseSpaceReport(ok({ ...REPORT, top: [] })))).toEqual([])
    expect(SPACE_TEXTS.topUnknown).toBe('Не удалось узнать, чем занято место.')
  })
})

describe('cleanOutcomeText', () => {
  const run = (v = {}) => ok({ installed: false, free_kb: 0, last_status: 'ok', ...v })
  it('освобождено -- по месту до и после', () => {
    expect(cleanOutcomeText({ run: run(), before: { freeKB: 100 * 1024 }, after: { freeKB: 130 * 1024 } })).toBe('Очистка выполнена, освобождено 30 МБ.')
  })
  it('места не прибавилось', () => {
    expect(cleanOutcomeText({ run: run(), before: { freeKB: 1000 }, after: { freeKB: 1000 } })).toBe('Очистка выполнена, свободного места не прибавилось.')
  })
  it('нет замера до или после -- без числа: last_freed_kb очистки -- это память, а не место', () => {
    expect(cleanOutcomeText({ run: run({ last_freed_kb: 2048 }), before: null, after: null })).toBe('Очистка выполнена.')
    expect(cleanOutcomeText({ run: run(), before: null, after: null })).toBe('Очистка выполнена.')
  })
  it('пропущена и ошибка очистки', () => {
    expect(cleanOutcomeText({ run: run({ last_status: 'skipped' }) })).toBe('Очистка пропущена — подробности в журнале очистки.')
    expect(cleanOutcomeText({ run: { status: 'err', output: 'boom' } })).toBe('Роутер ответил ошибкой — подробности ниже.')
    expect(cleanOutcomeText({ run: { status: 'timeout' } })).toBe('Роутер не ответил вовремя — проверьте место ещё раз.')
    expect(cleanOutcomeText({ run: null })).toBe('')
  })
})

describe('spaceFailureText', () => {
  it('отказы отчёта словами', () => {
    expect(spaceFailureText({ status: 'err', output: 'unknown action: space_report' })).toBe(AGENT_OLDER_THAN_APP)
    expect(spaceFailureText({ status: 'timeout' })).toBe('Роутер не ответил вовремя — проверьте место ещё раз.')
    expect(spaceFailureText({ status: 'err', output: 'df failed' })).toBe('Роутер не смог посчитать место — подробности ниже.')
    expect(spaceFailureText({ status: 'ok', output: 'x' })).toBe('Роутер ответил непонятно — проверьте место ещё раз.')
    expect(spaceFailureText(ok(REPORT))).toBe('')
  })
})
