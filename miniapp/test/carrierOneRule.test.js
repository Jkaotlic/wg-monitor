import { describe, it, expect } from 'vitest'
import { CARRIER_SCENARIOS } from './fixtures/carrier_screens.js'
import { pathState, reserveLine, withSnapshotCarrier } from '../src/trafficPath.js'
import { routerHeadline } from '../src/routerHeadline.js'
import { routingVerdict, withCheckVerdict } from '../src/routes.js'
import { tunnelsView } from '../src/tunnelsView.js'

// v0.56, спека B1: «Роутер» и «VPN-туннели» называют один и тот же несущий
// VPN-туннель -- тот, что назвал сервер (traffic.carrier_tunnel_id), а когда
// сервер не знает -- активное звено из снимка маршрутов роутера (экран
// «Роутер» берёт недавний снимок, который сняла вкладка), -- и не угадывают
// его по «первому поднятому».

function routerScreen(s) {
  const { router, incidents, reserveOnlyAlert } = s
  const { tunnels } = s.events
  const traffic = withSnapshotCarrier(s.events.traffic, s.snapshot)
  const headline = routerHeadline({ router, traffic, incidents, tunnels, reserveOnlyAlert })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  const reserve = reserveLine({ traffic, incidents, tunnels, via: path.via })
  return { headline, path, reserve }
}

function tabScreen(s) {
  return tunnelsView(withCheckVerdict(s.snapshot, s.checks), s.events.traffic)
}

// «Маршруты»: тот же снимок с вердиктом проверок и тот же traffic.
function routesScreen(s) {
  return routingVerdict(withCheckVerdict(s.snapshot, s.checks), s.events.traffic)
}

// Имена живых VPN-туннелей сценария, которых экран не вправе назвать
// несущим, когда несущий неизвестен.
const allNames = (s) => s.events.tunnels.map((t) => t.name)

describe.each(CARRIER_SCENARIOS)('B1: $title', (s) => {
  const r = routerScreen(s)
  const v = tabScreen(s)
  const rv = routesScreen(s)

  it('фикстура сама с собой согласна: один id -- одно имя в проверках и в снимке', () => {
    const snapNames = new Map(s.snapshot.tunnels.map((t) => [t.id, t.name]))
    for (const t of s.events.tunnels) {
      if (snapNames.has(t.tunnel_id)) expect(snapNames.get(t.tunnel_id)).toBe(t.name)
    }
  })

  it('«Маршруты» говорят о несущем то же, что «Роутер» и «VPN-туннели»', () => {
    if (s.expect.name) {
      expect(rv.title).toContain(`«${s.expect.name}»`)
      if (s.expect.alive) expect(rv.title).not.toMatch(/не отвечает/)
      else expect(rv.title).toMatch(/не отвечает/)
    } else {
      for (const name of allNames(s)) expect(`${rv.title} ${rv.detail}`).not.toContain(`«${name}»`)
    }
  })

  it('заголовок звена -- его состояние, а не подпись режима', () => {
    for (const c of v.chain) expect(['active', 'activeDown', 'activeUnknown', 'ready', 'down', 'off', 'unknown', 'checkUnknown']).toContain(c.role)
  })

  if (s.expect.name) {
    it('оба экрана называют одного несущего', () => {
      expect(r.path.via).toBe(s.expect.name)
      expect(v.active?.title).toBe(s.expect.name)
    })

    if (s.expect.alive) {
      it('несущий жив на обоих экранах', () => {
        expect(r.path.tunnel).toBe('up')
        expect(v.active.live).toBe('up')
        expect(r.headline.verdict).toContain(`«${s.expect.name}»`)
      })
    } else {
      it('несущий «несёт, но не отвечает» -- не «работает» ни на одном экране', () => {
        expect(r.path.tunnel).toBe('down')
        expect(r.headline.tag).not.toMatch(/^всё работает/)
        expect(r.headline.tone).not.toBe('sig')
        expect(v.active.live).toBe('down')
        expect(v.chain.find((c) => c.tunnelID === v.active.id)?.role).toBe('activeDown')
      })
    }
  } else {
    it('несущий не назван ни на одном экране', () => {
      expect(r.path.via).toBe('')
      expect(r.path.latencyMs).toBeNull()
      expect(v.active).toBeNull()
      expect(v.chain.some((c) => c.role === 'active' || c.role === 'activeDown')).toBe(false)
    })

    it('заголовок «Роутера» не называет обходом ни один VPN-туннель и не гадает «на запасном»', () => {
      for (const name of allNames(s)) {
        expect(r.headline.verdict).not.toMatch(new RegExp(`через «${name}»`))
      }
      expect(r.headline.tag).not.toMatch(/запасн/)
    })
  }

  if (s.expect.headlineTone) {
    it('тон шапки «Роутера»', () => {
      expect(r.headline.tone).toBe(s.expect.headlineTone)
    })
  }
  if (s.expect.headlineTag) {
    it('тег шапки «Роутера»', () => {
      expect(r.headline.tag).toBe(s.expect.headlineTag)
      expect(r.headline.verdict).toContain(`«${s.expect.name}»`)
    })
  }
  if (s.expect.name) {
    it('шапка не говорит «роутер не сообщил» и «напрямую через провайдера» при известном несущем', () => {
      expect(r.headline.verdict).not.toMatch(/не сообщил/)
      expect(r.headline.verdict).not.toMatch(/всё идёт напрямую через провайдера/)
    })
  }

  if (s.singbox) {
    it('sing-box: вкладка говорит «настроено, маршрут выбирается по адресу», звенья -- по состоянию', () => {
      expect(v.state).toBe('singbox')
      const link = v.chain.find((c) => c.tunnelID === s.snapshot.policies[0].active_tunnel_id)
      expect(link.role).toBe('down')
      expect(rv.title).toMatch(/sing-box/)
    })
  } else if (!s.expect.name) {
    it('несущий неизвестен: вкладка говорит «не знаем», а не «ни один не несёт»', () => {
      expect(v.state).toBe('unknown')
    })
  }
})

describe('B1: снимок маршрутов для экрана «Роутер»', () => {
  it('недавний снимок отдаётся, устаревший (старше 2 минут) -- нет', async () => {
    const { rememberRouteSnapshot, recentRouteSnapshot } = await import('../src/routes.js')
    const snap = { tunnels: [], policies: [] }
    rememberRouteSnapshot(91, snap, 1_000)
    expect(recentRouteSnapshot(91, 1_000 + 110_000)).toBe(snap)
    expect(recentRouteSnapshot(91, 1_000 + 130_000)).toBeNull()
    expect(recentRouteSnapshot(92, 1_000)).toBeNull()
  })

  it('опоздавший ответ прошлого роутера ложится под его id, не под открытый', async () => {
    const { rememberCommandSnapshot, recentRouteSnapshot } = await import('../src/routes.js')
    const old = { tunnels: [{ id: 'awg1' }], policies: [] }
    // Открыт роутер 95, а ответ пришёл на вопрос, заданный роутеру 94.
    const shown = rememberCommandSnapshot(95, { status: 'ok', for_router: 94 }, old, 5_000)
    expect(shown).toBe(false)
    expect(recentRouteSnapshot(94, 5_000)).toBe(old)
    expect(recentRouteSnapshot(95, 5_000)).toBeNull()
    // Ответ про открытый роутер -- под его id и на экран.
    const fresh = { tunnels: [], policies: [] }
    expect(rememberCommandSnapshot(95, { status: 'ok', for_router: 95 }, fresh, 6_000)).toBe(true)
    expect(recentRouteSnapshot(95, 6_000)).toBe(fresh)
  })

  it('сервер знает несущего -- снимок его не перебивает', () => {
    const traffic = { mode: 'split', carrier_tunnel_id: 'awg10', carrier_basis: 'policy', carrier_alive: true }
    const snap = { tunnels: [{ id: 'awg14' }], policies: [{ active_tunnel_id: 'awg14', via_vpn: true }] }
    expect(withSnapshotCarrier(traffic, snap)).toBe(traffic)
  })

  it('на sing-box и «напрямую» снимок несущего не назначает', () => {
    const snap = { tunnels: [{ id: 'awg14' }], policies: [{ active_tunnel_id: 'awg14', via_vpn: true }] }
    for (const mode of ['singbox', 'direct']) {
      const traffic = { mode, carrier_basis: 'none', carrier_alive: false }
      expect(withSnapshotCarrier(traffic, snap)).toBe(traffic)
    }
  })
})
