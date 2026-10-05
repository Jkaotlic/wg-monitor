import { describe, it, expect } from 'vitest'
import { CARRIER_SCENARIOS } from './fixtures/carrier_screens.js'
import { pathState, reserveLine } from '../src/trafficPath.js'
import { routerHeadline } from '../src/routerHeadline.js'
import { withCheckVerdict } from '../src/routes.js'
import { tunnelsView } from '../src/tunnelsView.js'

// v0.56, спека B1: «Роутер» и «VPN-туннели» называют один и тот же несущий
// VPN-туннель -- тот, что назвал сервер (traffic.carrier_tunnel_id), -- и
// не угадывают его по «первому поднятому».

function routerScreen(s) {
  const { router, incidents, reserveOnlyAlert } = s
  const { tunnels, traffic } = s.events
  const headline = routerHeadline({ router, traffic, incidents, tunnels, reserveOnlyAlert })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  const reserve = reserveLine({ traffic, incidents, tunnels, via: path.via })
  return { headline, path, reserve }
}

function tabScreen(s) {
  return tunnelsView(withCheckVerdict(s.snapshot, s.checks), s.events.traffic)
}

// Имена живых VPN-туннелей сценария, которых экран не вправе назвать
// несущим, когда несущий неизвестен.
const allNames = (s) => s.events.tunnels.map((t) => t.name)

describe.each(CARRIER_SCENARIOS)('B1: $title', (s) => {
  const r = routerScreen(s)
  const v = tabScreen(s)

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

    it('заголовок «Роутера» не называет обходом ни один VPN-туннель', () => {
      for (const name of allNames(s)) {
        expect(r.headline.verdict).not.toMatch(new RegExp(`через «${name}»`))
      }
    })
  }

  if (s.singbox) {
    it('sing-box: вкладка говорит «настроено, маршрут выбирается по адресу», а не «несёт»', () => {
      expect(v.state).toBe('singbox')
      const link = v.chain.find((c) => c.tunnelID === s.snapshot.policies[0].active_tunnel_id)
      expect(link.role).toBe('routed')
    })
  } else if (!s.expect.name) {
    it('несущий неизвестен: вкладка говорит «не знаем», а не «ни один не несёт»', () => {
      expect(v.state).toBe('unknown')
    })
  }
})
