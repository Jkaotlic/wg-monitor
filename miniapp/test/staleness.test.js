// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { isStale, reachStatus } from '../src/staleness.js'
import { routerHeadline } from '../src/routerHeadline.js'
import { fleetRow, fleetSummary, sortByUrgency } from '../src/fleet.js'
import { filterBucket } from '../src/fleetFilter.js'
import { checkRows } from '../src/diag.js'
import { routerContext } from '../src/screens/OverlayHost.jsx'
import { isAway } from '../src/agentUpdate.js'

// Форма строки /v1/miniapp/routers (miniappRouterSummary): status остаётся
// «alert», пока открыта тревога, а молчание сервер сообщает полем stale
// (v0.46, MINI-01). 27.09: роутер с тревогой молчал 47 часов, а экран писал
// в настоящем времени.
const SILENT_ALERT = {
  id: 7,
  nickname: 'tihiy-dom',
  kind: 'static',
  status: 'alert',
  stale: true,
  last_seen_age_sec: 47 * 3600,
  active_incidents: [{ check_name: 'dns' }],
}
const LIVE_ALERT = { ...SILENT_ALERT, id: 8, nickname: 'zhivoy', stale: false, last_seen_age_sec: 40 }

describe('isStale -- одно правило давности на весь экран', () => {
  it('stale от сервера важнее статуса alert', () => {
    expect(isStale(SILENT_ALERT)).toBe(true)
    expect(isStale(LIVE_ALERT)).toBe(false)
  })
  it('offline и sleeping устарели всегда', () => {
    expect(isStale({ status: 'offline' })).toBe(true)
    expect(isStale({ status: 'sleeping' })).toBe(true)
  })
  it('старый бэкенд без поля stale -- давность только по статусу', () => {
    expect(isStale({ status: 'alert', last_seen_age_sec: 999999 })).toBe(false)
    expect(isStale({ status: 'online' })).toBe(false)
    expect(isStale(null)).toBe(false)
  })
  it('reachStatus сворачивает молчащую тревогу в «не на связи»', () => {
    expect(reachStatus(SILENT_ALERT)).toBe('offline')
    expect(reachStatus({ ...SILENT_ALERT, kind: 'mobile' })).toBe('sleeping')
    expect(reachStatus(LIVE_ALERT)).toBe('alert')
    expect(reachStatus({ status: 'online' })).toBe('online')
  })
})

describe('MINI-01: молчащий роутер с тревогой не выглядит живым', () => {
  it('шапка говорит «не отвечает», а не называет тревогу в настоящем времени', () => {
    const h = routerHeadline({ router: SILENT_ALERT, incidents: SILENT_ALERT.active_incidents })
    expect(h.stale).toBe(true)
    expect(h.tag).toContain('роутер не отвечает')
  })
  it('живой роутер с тревогой -- прежняя шапка про поломку', () => {
    const h = routerHeadline({ router: LIVE_ALERT, incidents: LIVE_ALERT.active_incidents })
    expect(h.stale).toBe(false)
    expect(h.tag).not.toContain('не отвечает')
  })
  it('строка списка: пилюля про молчание, не «тревога»', () => {
    const row = fleetRow(SILENT_ALERT)
    expect(row.pill.text).toContain('молчит')
    expect(fleetRow(LIVE_ALERT).pill.text).toBe('тревога')
  })
  it('сводка широкого экрана считает его молчащим', () => {
    const s = fleetSummary([SILENT_ALERT, LIVE_ALERT])
    expect(s.silent).toBe(1)
    expect(s.attention).toBe(1)
  })
  it('сортировка: молчащая тревога идёт с молчащими, живая тревога выше', () => {
    const sorted = sortByUrgency([SILENT_ALERT, LIVE_ALERT])
    expect(sorted[0].nickname).toBe('zhivoy')
  })
  it('фильтр «молчат» включает stale', () => {
    expect(filterBucket(SILENT_ALERT)).toBe('silent')
    expect(filterBucket(LIVE_ALERT)).toBe('alert')
  })
  it('строка «Роутер отчитался о себе» -- «нет»', () => {
    const rows = checkRows({ checks: [], tunnels: [], router: SILENT_ALERT })
    const hb = rows.find((r) => r.key === 'agent_heartbeat')
    expect(hb.answer).toBe('нет')
  })
  it('слой поверх вкладок обещает отложенный ответ', () => {
    expect(routerContext([SILENT_ALERT], 7).asleep).toBe(true)
    expect(routerContext([LIVE_ALERT], 8).asleep).toBe(false)
  })
  it('обновление агента ждёт включения', () => {
    expect(isAway({ status: 'alert', stale: true, last_seen_age_sec: 120 })).toBe(true)
  })
})

describe('MINI-01: строка «Парка» у молчащей тревоги', () => {
  // Форма строки /v1/miniapp/fleet (miniappFleetRouter): incidents --
  // список имён проверок, away -- «не на связи» по правилу сервера.
  it('говорит, сколько роутер молчит, а не только имя тревоги', async () => {
    const { fleetRouterRows } = await import('../src/fleetAdmin.js')
    const rows = fleetRouterRows({
      routers: [{ id: 3, nickname: 'tihiy-dom', status: 'alert', away: true, last_seen_age_sec: 2 * 86400, incidents: ['dns'] }],
    })
    expect(rows[0].sub).toMatch(/^не на связи/)
  })
})

// review v0.46, п. 4: сервер отдаёт reach ("online"|"sleeping"|"offline"),
// посчитанный без учёта тревог. Когда он есть -- он единственный источник.
describe('reach от сервера -- один источник для подписи и фильтра', () => {
  const MOBILE_SLEEPING_ALERT = {
    id: 11, nickname: 'vymysel-car', kind: 'mobile', status: 'alert', stale: true, reach: 'sleeping',
    last_seen_age_sec: 3 * 3600, active_incidents: [{ check_name: 'dns' }],
  }
  it('reach важнее догадки по kind', () => {
    expect(reachStatus({ ...MOBILE_SLEEPING_ALERT, kind: 'static' })).toBe('sleeping')
    expect(reachStatus({ ...SILENT_ALERT, kind: 'mobile', reach: 'offline' })).toBe('offline')
    expect(isStale({ status: 'alert', reach: 'offline' })).toBe(true)
    expect(isStale({ status: 'alert', reach: 'online', stale: true })).toBe(false)
  })
  it('фильтр кладёт роутер туда же, куда его подписывает пилюля', () => {
    const pill = fleetRow(MOBILE_SLEEPING_ALERT).pill.text
    expect(pill).toMatch(/^спит/)
    expect(filterBucket(MOBILE_SLEEPING_ALERT)).toBe('sleeping')
    const off = { ...SILENT_ALERT, reach: 'offline' }
    expect(fleetRow(off).pill.text).toMatch(/^молчит/)
    expect(filterBucket(off)).toBe('silent')
  })
  it('без reach: мобильная молчащая тревога -- «спит» и в фильтре «спят»', () => {
    const m = { ...SILENT_ALERT, kind: 'mobile' }
    expect(fleetRow(m).pill.text).toMatch(/^спит/)
    expect(filterBucket(m)).toBe('sleeping')
  })
})
