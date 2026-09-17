import { describe, it, expect } from 'vitest'
import { watchdogLine, routerDelayLines } from '../src/watchdogLine.js'

const WD = {
  alive: true,
  reason: 'обход 40s назад',
  last_scan_at: '2026-09-17T10:00:00Z',
  offline_errors: 0,
  scans_total: 1234,
  stale_users: 2,
  suppressed_users: 1,
  last_scan_ms: 85,
}
const fleet = (wd, generated = '2026-09-17T10:00:40Z') => ({ generated_at: generated, watchdog: wd })

describe('строка «Сторож»', () => {
  it('когда был обход, сколько молчит и сколько заглушено', () => {
    expect(watchdogLine(fleet(WD))).toEqual({
      text: 'последний обход 40 с назад · молчат 2 · заглушено 1',
      sub: '1234 обхода с запуска · обход занял 85 мс',
      tone: 'ok',
      alarm: '',
    })
  })

  it('давний обход -- минутами, «назад» считается от generated_at', () => {
    expect(watchdogLine(fleet(WD, '2026-09-17T10:05:00Z')).text).toContain('последний обход 5 мин назад')
  })

  it('обхода не было -- так и сказано', () => {
    expect(watchdogLine(fleet({ ...WD, last_scan_at: '' })).text).toMatch(/^обхода ещё не было/)
  })

  it('сорванные отправки -- предупреждение, мёртвый сторож -- тревога с причиной', () => {
    const warn = watchdogLine(fleet({ ...WD, offline_errors: 3 }))
    expect(warn.tone).toBe('warn')
    expect(warn.sub).toContain('3 отправки не ушли')
    const dead = watchdogLine(fleet({ ...WD, alive: false, reason: 'сторож не обходил парк 5m0s' }))
    expect(dead.tone).toBe('danger')
    expect(dead.alarm).toBe('сторож не обходил парк 5m0s')
  })

  it('старый бэкенд без новых счётчиков -- не выдумываем нули', () => {
    const old = watchdogLine(fleet({ alive: true, reason: '', last_scan_at: '2026-09-17T10:00:00Z', offline_errors: 0 }))
    expect(old.text).toBe('последний обход 40 с назад')
    expect(old.sub).toBe('')
  })

  it('сторожа в сборке нет -- строки нет', () => {
    expect(watchdogLine({})).toBe(null)
    expect(watchdogLine(null)).toBe(null)
  })
})

describe('строки отложенного в строке роутера', () => {
  const tz = { timeZone: 'UTC' }

  it('ждёт обновления с …', () => {
    expect(routerDelayLines({ pending_version: 'v0.36.0', pending_since: '2026-09-12T14:20:00Z' }, tz)).toEqual([
      { key: 'pending', tone: 'muted', text: 'ждёт обновления с 12.09 14:20' },
    ])
  })

  it('последняя раскатка: прошла, не прошла, итог неизвестен', () => {
    const at = '2026-09-15T08:05:00Z'
    expect(routerDelayLines({ last_deploy: { version: 'v0.35.0', at, ok: true } }, tz)).toEqual([
      { key: 'deploy', tone: 'ok', text: 'последняя раскатка v0.35.0 · 15.09 08:05 · прошла' },
    ])
    expect(routerDelayLines({ last_deploy: { version: 'v0.35.0', at, ok: false } }, tz)[0]).toEqual({
      key: 'deploy',
      tone: 'danger',
      text: 'последняя раскатка v0.35.0 · 15.09 08:05 · не прошла',
    })
    expect(routerDelayLines({ last_deploy: { version: 'v0.35.0', at } }, tz)[0].text).toBe('последняя раскатка v0.35.0 · 15.09 08:05')
  })

  it('тревога с … (N раз), с правильным склонением', () => {
    const inc = (n) => routerDelayLines({ incident: { hard_since: '2026-09-17T06:00:00Z', fail_count: n } }, tz)[0].text
    expect(inc(5)).toBe('тревога с 17.09 06:00 (5 раз)')
    expect(inc(2)).toBe('тревога с 17.09 06:00 (2 раза)')
    expect(inc(1)).toBe('тревога с 17.09 06:00 (1 раз)')
    expect(inc(0)).toBe('тревога с 17.09 06:00')
  })

  it('порядок: ожидание, раскатка, тревога; пустые поля -- без строк', () => {
    const lines = routerDelayLines(
      {
        pending_since: '2026-09-12T14:20:00Z',
        last_deploy: { version: 'v0.35.0', at: '2026-09-15T08:05:00Z', ok: true },
        incident: { hard_since: '2026-09-17T06:00:00Z', fail_count: 5 },
      },
      tz,
    )
    expect(lines.map((l) => l.key)).toEqual(['pending', 'deploy', 'incident'])
    expect(routerDelayLines({ pending_since: null, last_deploy: null, incident: null }, tz)).toEqual([])
    expect(routerDelayLines({ incident: { hard_since: '', fail_count: 3 } }, tz)).toEqual([])
    expect(routerDelayLines(undefined, tz)).toEqual([])
  })
})
