import { describe, it, expect } from 'vitest'
import { serverClockOffset } from '../src/serverClock.js'
import { checkRows } from '../src/diag.js'

// MINI-10: возраст «измерено» не по часам телефона. Сервер в каждом ответе
// про роутер отдаёт пару last_seen_at + last_seen_age_sec (посчитанную по
// СВОИМ часам) -- из неё и берётся сдвиг часов телефона. Имена вымышленные.
const SERVER_NOW = Date.parse('2026-09-27T12:00:00Z')
const ROUTER = { id: 3, nickname: 'vymysel', status: 'online', stale: false, last_seen_at: '2026-09-27T11:59:30Z', last_seen_age_sec: 30 }
const CHECKS = [{ check_name: 'dns', status: 'ok', ts: '2026-09-27T11:55:00Z' }]

describe('MINI-10', () => {
  it('сдвиг часов из last_seen_at + last_seen_age_sec', () => {
    // Телефон отстаёт на час.
    const phoneNow = SERVER_NOW - 3600_000
    expect(serverClockOffset(ROUTER, phoneNow)).toBe(3600_000)
    expect(serverClockOffset({ status: 'online' }, phoneNow)).toBe(null)
  })

  it('телефон отстаёт на час -- «измерено 5 мин назад», а не «0 сек»', () => {
    const phoneNow = SERVER_NOW - 3600_000
    const rows = checkRows({ checks: CHECKS, tunnels: [], router: ROUTER, clockOffsetMs: 3600_000, nowMs: phoneNow })
    expect(rows.find((r) => r.key === 'dns').value).toBe('измерено 5 мин назад')
  })

  it('телефон спешит на сутки, сдвига нет -- время словами, а не выдуманный возраст', () => {
    const phoneNow = SERVER_NOW + 86400_000
    const rows = checkRows({ checks: CHECKS, tunnels: [], router: { status: 'online' }, nowMs: phoneNow })
    const v = rows.find((r) => r.key === 'dns').value
    expect(v).not.toContain('назад')
    expect(v).toMatch(/^измерено \d{1,2} [а-я]{3}( \d{4})?, \d\d:\d\d$/)
  })
})

describe('MINI-10: «последний раз смотрели» в настройках', () => {
  it('время словами, а не возраст по часам телефона', async () => {
    const { checkedAtText, unknownLine } = await import('../src/versions.js')
    const text = checkedAtText('2026-09-27T09:05:00Z')
    expect(text).toMatch(/^27 сен( \d{4})?, \d\d:\d\d$/)
    expect(checkedAtText(undefined)).toBe('')
    expect(unknownLine('upstream_unavailable', text)).toContain(`последний раз смотрели ${text}`)
  })
})
