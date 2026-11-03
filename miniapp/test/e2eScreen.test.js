import { describe, it, expect } from 'vitest'
import S1 from './fixtures/e2e/s1_dead_carrier.json'
import S1soft from './fixtures/e2e/s1_below_threshold.json'
import S2 from './fixtures/e2e/s2_idle_reserve.json'
import S5 from './fixtures/e2e/s5_dead_reserve.json'
import S11 from './fixtures/e2e/s11_awgm_silent.json'
import S12 from './fixtures/e2e/s12_agent_silent.json'
import S13 from './fixtures/e2e/s13_moved.json'
import { pathState, reserveLine, deadReserveLine, carrierAlive } from '../src/trafficPath.js'
import { routerHeadline } from '../src/routerHeadline.js'
import { fleetRow } from '../src/fleet.js'

// Экран глазами человека: снимки -- настоящие ответы мини-аппа из контура
// internal/e2e (screen_fixture_test.go сверяет их с контуром), функции --
// настоящие функции экрана, в том же порядке, что в RouterDetail.jsx.
function screen(fx) {
  const { router, traffic, tunnels } = fx
  const incidents = fx.incidents ?? []
  const headline = routerHeadline({ router, traffic, incidents, tunnels, reserveOnlyAlert: fx.reserve_only_alert })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  const reserve = reserveLine({ traffic, tunnels, incidents, via: path.via })
  const deadReserve = reserve ? null : deadReserveLine({ traffic, tunnels, incidents, via: path.via })
  const row = fleetRow({ ...router, reserve_only_alert: fx.reserve_only_alert })
  return { headline, path, reserve, deadReserve, row, words: `${headline.tag} ${headline.verdict}` }
}

describe('S1: несущий обменивается ключами, а проба через него падает', () => {
  for (const [name, fx] of [['ниже порога', S1soft], ['после порога', S1]]) {
    const s = screen(fx)
    it(`${name}: заголовок не «всё работает»`, () => {
      expect(s.headline.tag).not.toBe('всё работает')
      expect(s.headline.tone).not.toBe('sig')
    })
    it(`${name}: мёртвый туннель не назван живым`, () => {
      expect(s.path.tunnel).toBe('down')
      expect(s.words).not.toMatch(/открывается через «vpn-main»/)
    })
    it(`${name}: живые запасные названы как запасные, а не как обход`, () => {
      expect(s.words).not.toMatch(/работает через «vpn-backup/)
    })
  }
})

describe('S2: резерв простаивает, проба жива', () => {
  const s = screen(S2)
  it('без ложной тревоги', () => {
    expect(s.headline.tone).toBe('sig')
    expect(s.headline.tag).toBe('всё работает')
    expect(s.path.tunnel).toBe('up')
    expect(s.row.pill?.text ?? '').not.toMatch(/тревога/)
  })
  it('обход назван верно, резерв готов', () => {
    expect(s.path.via).toBe('vpn-main')
    expect(s.reserve).toBeTruthy()
    expect(s.deadReserve).toBeFalsy()
  })
})

describe('S5: мёртв запасной, несущий жив', () => {
  const s = screen(S5)
  it('обход не перекрашен красным', () => {
    expect(s.path.tunnel).toBe('up')
    expect(s.path.via).toBe('vpn-main')
    expect(s.headline.tone).not.toBe('danger')
  })
  it('мёртвый запасной не назван живым', () => {
    expect(s.words).not.toMatch(/через «vpn-backup-one»/)
    expect(JSON.stringify(s.reserve ?? '')).not.toMatch(/vpn-backup-one/)
  })
  it('в списке роутеров -- не красная тревога', () => {
    expect(S5.reserve_only_alert).toBe(true)
    expect(s.row.pill?.text ?? '').not.toBe('тревога')
  })
})

describe('S11: awg-manager молчит', () => {
  const s = screen(S11)
  it('заголовок не «всё работает»', () => {
    expect(s.headline.tag).not.toBe('всё работает')
    expect(s.headline.tone).toBe('danger')
  })
})

describe('S12: агент молчит', () => {
  const s = screen(S12)
  const incidents = S12.incidents ?? []
  it('заголовок -- молчание, а не «всё работает»', () => {
    expect(s.headline.stale).toBe(true)
    expect(s.headline.tag).toMatch(/роутер не отвечает/)
    expect(s.words).toMatch(/данные на тот момент/)
  })
  it('последний зелёный отчёт не выдан за текущий', () => {
    expect(s.path.tunnel).not.toBe('up')
    expect(s.path.direct).toBe('unknown')
  })
  it('carrier_alive из последнего отчёта не красит схему живой', () => {
    // Сервер отвечает про последний отчёт; решает stale на экране.
    expect(s.path.tunnel).toBe('unknown')
    expect(carrierAlive({ traffic: S12.traffic, tunnels: S12.tunnels, incidents })).toBe(true)
  })
})

describe('S13: трафик уведён на живой резерв', () => {
  const s = screen(S13)
  it('обход идёт через резерв, и экран это знает', () => {
    expect(s.path.tunnel).toBe('up')
    expect(s.path.via).toBe('vpn-backup-one')
  })
  it('упавший не назван несущим', () => {
    expect(S13.traffic.carrier_tunnel_id).toBe('awg10')
    expect(s.words).not.toMatch(/открывается через «vpn-main»/)
  })
  it('в списке роутеров -- не красная тревога', () => {
    expect(S13.reserve_only_alert).toBe(true)
  })
})
