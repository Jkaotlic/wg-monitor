import { describe, it, expect } from 'vitest'
import { checkLabel, incidentCopy, tunnelOf } from '../src/labels.js'
import { backupCopy, deadReserveLine, reserveLine } from '../src/trafficPath.js'

const TUNNELS = [
  { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
  { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok' },
]
const INC = [{ check_name: 'tunnel_awg10' }]

describe('тревога называет VPN-туннель именем (спека п. 1.3)', () => {
  it('tunnelOf: имя из списка, id -- отдельно', () => {
    expect(tunnelOf('tunnel_awg10', TUNNELS)).toEqual({ id: 'awg10', name: 'vpn-nl' })
    expect(tunnelOf('tunnel_awg99', TUNNELS)).toEqual({ id: 'awg99', name: '' })
    expect(tunnelOf('dns', TUNNELS)).toBe(null)
  })

  it('checkLabel: с именем -- «имя», без списка -- как раньше', () => {
    expect(checkLabel('tunnel_awg10', TUNNELS)).toBe('VPN-туннель «vpn-nl»')
    expect(checkLabel('tunnel_awg10')).toBe('VPN-туннель awg10')
  })

  it('incidentCopy: имя в заголовке, awgNN -- кодом рядом', () => {
    const c = incidentCopy('tunnel_awg10', TUNNELS)
    expect(c.what).toBe('VPN-туннель «vpn-nl» не отвечает')
    expect(c.code).toBe('awg10')
  })

  it('incidentCopy: имени нет -- id в заголовке, кода нет', () => {
    const c = incidentCopy('tunnel_awg99', TUNNELS)
    expect(c.what).toBe('VPN-туннель awg99 не отвечает')
    expect(c.code).toBe('')
  })

  it('incidentCopy: служба -- без изменений', () => {
    expect(incidentCopy('dns', TUNNELS).what).toBe('Не определяются адреса сайтов')
  })
})

describe('«запасного нет» против «запасной упал» (спека п. 1.3)', () => {
  const traffic = { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip' }

  it('мёртвый запасной находится, несущий -- нет', () => {
    expect(deadReserveLine({ traffic, tunnels: TUNNELS, incidents: INC, via: 'vpn-hip' })?.tunnel_id).toBe('awg10')
  })

  it('выключенный руками -- не «упал»', () => {
    const tunnels = [TUNNELS[1], { tunnel_id: 'awg11', name: 'vpn-off', run_state: 'stopped', status: 'ok' }]
    expect(deadReserveLine({ traffic, tunnels, incidents: [], via: 'vpn-hip' })).toBeUndefined()
  })

  it('запасной упал, герой про него говорит -- плитки нет', () => {
    const backupLine = reserveLine({ traffic, tunnels: TUNNELS, incidents: INC, via: 'vpn-hip' })
    expect(backupLine).toBeUndefined()
    expect(backupCopy({ backupLine, deadReserve: TUNNELS[0], heroCovers: true })).toBe(null)
  })

  it('запасной упал, герой молчит -- «не отвечает», а не «нет»', () => {
    const c = backupCopy({ backupLine: undefined, deadReserve: TUNNELS[0], heroCovers: false })
    expect(c.title).toBe('Запасной «vpn-nl» не отвечает')
    expect(c.tone).toBe('warn')
  })

  it('запасного правда нет -- прежняя плитка', () => {
    expect(backupCopy({ backupLine: undefined }).title).toBe('Запасного VPN-туннеля нет')
    expect(backupCopy({ backupLine: undefined, deadReserve: null, heroCovers: true }).title).toBe('Запасного VPN-туннеля нет')
  })
})
