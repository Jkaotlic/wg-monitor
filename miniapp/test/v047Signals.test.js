import { describe, it, expect, vi, afterEach } from 'vitest'
import { exitLine, wanView, hooksRow, nativeDNSView, logsView, pingFailsLine, SIGNAL_TEXTS } from '../src/signals.js'
import { incidentLine } from '../src/incidents.js'
import { fetchRouterFacts } from '../src/api.js'
import { editableAgentConfigKeys, agentConfigRows } from '../src/agentConfig.js'

const NOW = Date.parse('2026-09-28T07:12:00Z')
const facts = (extra = {}) => ({ supported: true, ping_fails_24h: {}, ...extra })

afterEach(() => vi.unstubAllGlobals())

describe('запрос фактов', () => {
  it('GET /routers/{id}/facts', async () => {
    const calls = []
    vi.stubGlobal('fetch', async (url) => {
      calls.push(url)
      return { ok: true, status: 200, json: async () => ({ supported: true }) }
    })
    expect(await fetchRouterFacts(2)).toEqual({ supported: true })
    expect(calls[0].endsWith('/routers/2/facts')).toBe(true)
  })
})

describe('адрес выхода', () => {
  it('старый агент -- так и говорим', () => {
    expect(exitLine({ supported: false }, 'awg11').value).toBe(SIGNAL_TEXTS.needAgent)
    expect(exitLine(null, 'awg11').value).toBe(SIGNAL_TEXTS.needAgent)
  })
  it('не мерили и остановлен -- разные слова', () => {
    expect(exitLine(facts({ exit: { tunnels: {} } }), 'awg11').value).toBe(SIGNAL_TEXTS.notMeasured)
    expect(exitLine(facts({ exit: { tunnels: {} } }), 'awg11', { running: false }).value).toBe(SIGNAL_TEXTS.stopped)
  })
  it('адреса разошлись -- оба адреса и давность', () => {
    const f = facts({ exit: { tunnels: { awg11: { vpn_ip: '203.0.113.7', direct_ip: '198.51.100.4', changed: true, at: '2026-09-28T07:00:00Z' } } } })
    const line = exitLine(f, 'awg11', { nowMs: NOW })
    expect(line.value).toBe('через VPN-туннель 203.0.113.7, напрямую 198.51.100.4')
    expect(line.sub).toBe('12 мин назад')
    expect(line.warn).toBe('')
  })
  it('адреса совпали -- жёлтым и с объяснением', () => {
    const f = facts({ exit: { tunnels: { awg11: { vpn_ip: '198.51.100.4', direct_ip: '198.51.100.4', changed: false, at: '2026-09-28T07:00:00Z' } } } })
    const line = exitLine(f, 'awg11', { nowMs: NOW })
    expect(line.tone).toBe('warn')
    expect(line.warn).toBe(SIGNAL_TEXTS.sameIP)
  })
  it('замер без вердикта', () => {
    const f = facts({ exit: { tunnels: { awg11: { failed: true, at: '2026-09-28T07:00:00Z' } } } })
    expect(exitLine(f, 'awg11', { nowMs: NOW }).value).toBe(SIGNAL_TEXTS.failed)
  })
})

describe('неудачи пингчека за сутки', () => {
  it('ноль не показываем: у VPN-туннеля без проверки связи ноль ничего не значит', () => {
    expect(pingFailsLine(facts({ ping_fails_24h: {} }), 'awg11')).toBeNull()
    expect(pingFailsLine({ supported: false }, 'awg11')).toBeNull()
  })
  it('неудачи есть -- число и жёлтый тон', () => {
    const line = pingFailsLine(facts({ ping_fails_24h: { awg11: 12 } }), 'awg11')
    expect(line).toEqual({ title: 'Неудачных проверок связи за сутки', value: '12', tone: 'warn' })
  })
})

describe('резервная линия', () => {
  const link = (role, up, pingcheck = '') => ({ label: role === 'primary' ? 'Подключение Ethernet' : 'Huawei Mobile Broadband', role, up, pingcheck })
  it('без резервного -- секции нет', () => {
    expect(wanView(facts({ wan: { links: [link('primary', true)] } }))).toBeNull()
  })
  it('резерв без Ping-Check -- подсказка', () => {
    const v = wanView(facts({ wan: { links: [link('primary', true), link('backup', false, 'unset')] } }))
    expect(v.rows[1].title).toBe('Резервное подключение')
    expect(v.rows[1].value).toBe('Huawei Mobile Broadband · не работает')
    expect(v.rows[1].sub).toBe('Ping-Check не задан')
    expect(v.hint).toBe(SIGNAL_TEXTS.noPingCheck)
  })
  it('о Ping-Check не знаем -- не пугаем', () => {
    const v = wanView(facts({ wan: { links: [link('primary', true), link('backup', false, '')] } }))
    expect(v.hint).toBe('')
    expect(v.rows[1].sub).toBe('')
  })
  it('старый awg-manager', () => {
    expect(wanView(facts({ wan: { unsupported: true, links: [] } })).note).toBe(SIGNAL_TEXTS.wanUnsupported)
  })
})

describe('мгновенная реакция', () => {
  it('состояния хука', () => {
    expect(hooksRow(facts({ hooks: { state: 'installed', last_wake_at: '2026-09-28T07:10:00Z' } }), NOW).value).toBe('включена · последний раз 2 мин назад')
    expect(hooksRow(facts({ hooks: { state: 'unsupported' } }), NOW).value).toBe('прошивка не умеет — роутер опрашивается по расписанию')
    expect(hooksRow(facts({ hooks: { state: 'disabled' } }), NOW).value).toBe('выключена в настройках агента')
    expect(hooksRow({ supported: false }, NOW).value).toBe(SIGNAL_TEXTS.needAgent)
  })
})

describe('списки сайтов прошивки', () => {
  it('имя VPN-туннеля, число сайтов и неработающая цель', () => {
    const v = nativeDNSView(
      facts({ native_dns: { lists: [
        { name: 'youtube', domains: 14, tunnel_id: 'awg11', owner: 'firmware', issue: 'target_down' },
        { name: 'one', domains: 1, owner: 'awgm' },
        { name: 'three', domains: 3, owner: 'awgm' },
      ] } }),
      [{ id: 'awg11', name: 'NL' }],
    )
    expect(v.rows[0].value).toBe('14 сайтов → VPN-туннель «NL»')
    expect(v.rows[0].sub).toBe('заведён в панели роутера')
    expect(v.rows[0].warn).toBe(SIGNAL_TEXTS.dnsTargetDown)
    expect(v.rows[1].value).toBe('1 сайт → интерфейс роутера')
    expect(v.rows[1].sub).toBe('завёл awg-manager')
    expect(v.rows[2].value).toBe('3 сайта → интерфейс роутера')
  })
  it('нет списков или старый агент -- секции нет', () => {
    expect(nativeDNSView(facts({ native_dns: { lists: [] } }), [])).toBeNull()
    expect(nativeDNSView({ supported: false }, [])).toBeNull()
  })
})

describe('журнал awg-manager', () => {
  const ok = (data) => ({ status: 'ok', output: JSON.stringify(data) })
  it('состояния ответа', () => {
    expect(logsView(ok({ unsupported: true, entries: [] })).note).toBe(SIGNAL_TEXTS.logsUnsupported)
    expect(logsView(ok({ enabled: false, entries: [] })).note).toBe(SIGNAL_TEXTS.logsDisabled)
    expect(logsView(ok({ enabled: true, entries: [] })).note).toBe(SIGNAL_TEXTS.logsEmpty)
    expect(logsView(null)).toBeNull()
  })
  it('записи: повторы и тон', () => {
    const v = logsView(ok({ enabled: true, entries: [{ ts: '2026-09-28T07:00:00Z', level: 'error', group: 'tunnel', action: 'start', message: 'не поднялся', repeats: 3 }] }))
    expect(v.rows[0].title).toBe('tunnel · start')
    expect(v.rows[0].value).toBe('не поднялся ×3')
    expect(v.rows[0].tone).toBe('danger')
  })
})

describe('«Что было» с awg-manager', () => {
  const base = { check_name: 'tunnel_awg11', from: '2026-09-28T00:12:00Z', to: '2026-09-28T00:14:00Z', down_sec: 120, flaps: 1, ongoing: false }
  it('моргнул между отчётами', () => {
    const line = incidentLine({ ...base, awgm_only: true, awgm_fails: 3 })
    expect(line.detail).toContain('по журналу awg-manager')
    expect(line.tone).toBe('warn')
    expect(line.ongoing).toBe(false)
  })
  it('подпись серии', () => {
    const line = incidentLine({ ...base, awgm_first_fail: '2026-09-28T00:10:00Z', awgm_fails: 5 })
    expect(line.detail).toContain('awg-manager: первая неудача в ')
    expect(line.detail).toContain('провалов 5')
  })
  it('awg-manager связь не терял', () => {
    expect(incidentLine({ ...base, awgm_clean: true }).detail).toContain('awg-manager связь не терял')
  })
})

describe('выключатель хука в настройках агента', () => {
  it('восьмой ключ формы и строка вида', () => {
    expect(editableAgentConfigKeys()).toContain('wake_hooks_off')
    const row = agentConfigRows({ wake_hooks_off: true }).find((r) => r.key === 'wake_hooks')
    expect(row.value).toBe('выключена')
  })
})

describe('словарь', () => {
  it('ни одна строка не обещает «весь трафик через VPN»', () => {
    for (const text of Object.values(SIGNAL_TEXTS)) {
      expect(text).not.toMatch(/весь трафик/i)
    }
  })
})
