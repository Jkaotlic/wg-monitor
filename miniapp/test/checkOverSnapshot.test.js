import { describe, it, expect } from 'vitest'
import sandbox from './fixtures/sandbox_counts.json'
import { tunnelStateLabel, tunnelTargetLabel } from '../src/labels.js'
import { tunnelRows, withCheckVerdict } from '../src/routes.js'
import { tunnelList } from '../src/tunnelDelete.js'
import { tunnelsView } from '../src/tunnelsView.js'

// Fix 3 (v0.56), правило A1.1 «проверка главнее» в обе стороны: на
// sandbox-broken снимок роутера говорит «vpn-de (awg10) down», а последняя
// проверка tunnel_awg10 -- ok, running, 52 мс. «Проверки» считали его
// работающим, вкладка писала «не отвечает», «Маршруты» -- без отметки.
const s = sandbox['sandbox-broken']
const shown = withCheckVerdict(s.snapshot, s.events)

describe('Fix 3: vpn-de одинаков на вкладке, «Проверках» и «Маршрутах»', () => {
  it('«Проверки» -- работает (исходная точка)', () => {
    expect(tunnelStateLabel(s.events.tunnels.find((t) => t.tunnel_id === 'awg10'))).toBe('работает')
  })
  it('вкладка: строка списка -- «работает»', () => {
    expect(tunnelList(shown, s.incidents).find((r) => r.id === 'awg10').stateLabel).toBe('работает')
  })
  it('вкладка: «Порядок подхвата» -- готов подхватить, а не «включён, но лежит»', () => {
    const link = tunnelsView(shown, s.events.traffic).chain.find((c) => c.tunnelID === 'awg10')
    expect(link.role).toBe('ready')
  })
  it('«Маршруты» -- «работает»', () => {
    expect(tunnelTargetLabel(tunnelRows(shown).find((r) => r.id === 'awg10'))).toBe('работает')
  })
  it('vpn-nl (проверка fail) не «работает» ни на одном', () => {
    expect(tunnelStateLabel(s.events.tunnels.find((t) => t.tunnel_id === 'awg12'))).not.toBe('работает')
    expect(tunnelList(shown, s.incidents).find((r) => r.id === 'awg12').stateLabel).not.toBe('работает')
    expect(tunnelTargetLabel(tunnelRows(shown).find((r) => r.id === 'awg12'))).not.toBe('работает')
  })
  it('нет строки проверки -- по снимку (vpn-spare up)', () => {
    expect(tunnelList(shown, s.incidents).find((r) => r.id === 'awg14').stateLabel).toBe('работает')
    const off = withCheckVerdict({ ...s.snapshot }, { tunnels: [] })
    expect(tunnelList(off).find((r) => r.id === 'awg10').stateLabel).toBe('не отвечает')
  })
  it('выключенный настройкой остаётся выключенным, даже если строка проверки ok', () => {
    const snap = { ...s.snapshot, tunnels: s.snapshot.tunnels.map((t) => (t.id === 'awg10' ? { ...t, enabled: false, status: 'disabled' } : t)) }
    const row = tunnelRows(withCheckVerdict(snap, s.events)).find((r) => r.id === 'awg10')
    expect(row.switchedOff).toBe(true)
    expect(row.live).toBe('down')
  })
})
