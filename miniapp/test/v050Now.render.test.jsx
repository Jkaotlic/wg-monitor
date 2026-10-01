// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import SNAP from './fixtures/split_reserve_dead.json'

const mocks = vi.hoisted(() => ({ router: null, checks: null, sent: 0, sendError: null, access: null }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchRouter: () => Promise.resolve(mocks.router),
    fetchRouterChecks: () => Promise.resolve(mocks.checks),
    fetchRouterVersions: () => Promise.resolve(null),
    fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
    fetchAccess: () => (mocks.access instanceof Error ? Promise.reject(mocks.access) : Promise.resolve(mocks.access)),
    sendCommand: () => {
      mocks.sent++
      return Promise.reject(mocks.sendError)
    },
  }
})

const { ApiError } = await import('../src/api.js')
const { FALLBACK_ERROR_TEXT } = await import('../src/errorText.js')
const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const { AccessSection } = await import('../src/screens/AccessSection.jsx')
const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { ExitCompareSection } = await import('../src/screens/ExitCompare.jsx')
const { AppContext } = await import('../src/appContext.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const buttons = (root, text) => [...root.querySelectorAll('button')].filter((b) => b.textContent.trim() === text)

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AppContext.Provider value={{ mode: 'miniapp', wide: false }}>{node}</AppContext.Provider>, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

beforeEach(() => {
  mocks.sent = 0
  mocks.sendError = new ApiError(400, 'unknown', '/routers/7/commands failed: 400')
  mocks.router = { router: structuredClone(SNAP.router), incidents: structuredClone(SNAP.incidents) }
  mocks.checks = { ...structuredClone(SNAP.events), traffic: { ...SNAP.events.traffic, egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip' } }
  mocks.access = { owner: null, operators: [] }
})

describe('ошибки словами (спека п. 1.4)', () => {
  it('«VPN-туннели»: отказ сервера без кода -- запасная фраза и «Повторить», пути нет', async () => {
    const root = await mount(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />)
    expect(root.textContent).not.toContain('failed')
    expect(root.textContent).not.toContain('/routers/')
    expect(root.textContent).toContain(FALLBACK_ERROR_TEXT)
    const retry = buttons(root, 'Повторить')
    expect(retry.length).toBeGreaterThan(0)
    const before = mocks.sent
    await act(async () => retry[0].click())
    await flush()
    expect(mocks.sent).toBe(before + 1)
    cleanup(root)
  })

  it('«Доступ»: отказ чтения -- словами', async () => {
    mocks.access = new ApiError(500, 'unknown', '/routers/7/access failed: 500')
    const root = await mount(<AccessSection routerID={7} openSheet={() => {}} />)
    expect(root.textContent).not.toContain('failed')
    expect(root.textContent).toContain(FALLBACK_ERROR_TEXT)
    cleanup(root)
  })

  it('в src не осталось ни одного err.message на экран', async () => {
    const { readdirSync, readFileSync } = await import('node:fs')
    const { fileURLToPath } = await import('node:url')
    const { dirname, join } = await import('node:path')
    // В jsdom import.meta.url -- не file:, и `new URL('../src/', …)` для
    // fileURLToPath не годится (см. park.render.test.jsx); путь собираем вручную.
    const dir = join(dirname(fileURLToPath(import.meta.url)), '../src') + '/'
    const files = readdirSync(dir, { recursive: true }).filter((f) => /\.(jsx?|mjs)$/.test(f))
    const hits = []
    for (const f of files) {
      // errorText.js сверяет message с COMMAND_GONE_TEXT -- это не вывод.
      if (f.endsWith('errorText.js')) continue
      const lines = readFileSync(dir + f, 'utf8').split('\n')
      lines.forEach((line, i) => {
        // signals.js: `e` -- строка журнала awg-manager, e.message -- её текст,
        // а не ошибка запроса; исключена ровно эта строка, не файл.
        if (f.endsWith('signals.js') && /`\$\{e\.message\} ×\$\{e\.repeats\}`|: e\.message,?$/.test(line.trim())) return
        // 1) любое имя перехваченной ошибки, что бы ни стояло в catch;
        // 2) `.catch((x) => …)` -- то же для промисов.
        const names = new Set()
        for (const m of line.matchAll(/catch\s*\(\s*(\w+)\s*\)/g)) names.add(m[1])
        for (const m of line.matchAll(/\(\s*(\w+)\s*\)\s*=>/g)) if (/\.catch\(/.test(line)) names.add(m[1])
        for (const n of names) if (new RegExp(`\\b${n}\\??\\.message\\b`).test(line)) hits.push(`${f}:${i + 1}: ${n}.message`)
      })
      // Тело catch-блока: имя из `catch (x) {` до закрывающей скобки блока.
      const text = lines.join('\n')
      for (const m of text.matchAll(/catch\s*\(\s*(\w+)\s*\)\s*\{/g)) {
        const body = text.slice(m.index, m.index + 600)
        const end = (() => { let d = 0; for (let k = body.indexOf('{'); k < body.length; k++) { if (body[k] === '{') d++; if (body[k] === '}' && --d === 0) return k } return body.length })()
        if (new RegExp(`\\b${m[1]}\\??\\.message\\b`).test(body.slice(0, end))) hits.push(`${f}: ${m[1]}.message в catch`)
      }
      // Любое `x.message` в строке, что рисует JSX (poll.message -- слово сервера
      // из jobPoll, не err.message; item.message -- поле ответа разбора .conf).
      lines.forEach((line, i) => {
        if (f.endsWith('signals.js') && /e\.message/.test(line)) return
        if (/<\w[^>]*>|\{[^}]*\}/.test(line) && /\b\w+\??\.message\b/.test(line) && f.endsWith('.jsx') && !/item\.message|m\.message|poll\.message/.test(line)) hits.push(`${f}:${i + 1}: jsx .message`)
      })
    }
    expect([...new Set(hits)]).toEqual([])
  })
})

async function mountNow() {
  const sheets = []
  const root = await mount(<RouterDetail id={56} openSheet={(s) => sheets.push(s)} onTab={() => {}} />)
  return { root, sheets }
}
const order = (root) => [...root.querySelector('.screen').children].map((el) => {
  if (el.classList.contains('hero')) return 'hero'
  if (el.querySelector?.('.incident-card')) return 'alerts'
  if (el.classList.contains('stat-grid')) return 'stats'
  if (el.classList.contains('maint-notice')) return 'maint'
  return el.className || el.tagName
})
const limes = (root) => [...root.querySelectorAll('.btn-primary')]

describe('«Сейчас» при тревоге (спека п. 1.1–1.3)', () => {
  it('порядок: герой → тревоги → остальное', async () => {
    const { root } = await mountNow()
    const o = order(root)
    expect(o[0]).toBe('hero')
    expect(o[1]).toBe('alerts')
    expect(o.indexOf('stats')).toBeGreaterThan(1)
    cleanup(root)
  })

  it('«Починить» -- единственный лайм, на всю ширину; перезапуск и «Не беспокоить…» -- пара', async () => {
    const { root } = await mountNow()
    const card = root.querySelector('.incident-card')
    const repair = buttons(card, 'Починить')[0]
    expect(repair.className).toContain('btn-primary')
    expect(repair.className).toContain('btn-wide')
    expect(limes(root)).toEqual([repair])
    const row = card.querySelector('.incident-actions-row')
    expect(row.className).toContain('action-row-pair')
    expect(buttons(row, 'Перезапустить VPN-туннель')[0].className).toContain('btn-ghost')
    expect(buttons(row, 'Не беспокоить…')[0].className).toContain('btn-ghost')
    expect(root.querySelector('.btn-accent')).toBe(null)
    cleanup(root)
  })

  it('тревога, которую назвала шапка, не повторяет объяснение над кнопкой', async () => {
    const { root } = await mountNow()
    expect(root.querySelector('.incident-card .incident-why')).toBe(null)
    cleanup(root)
  })

  it('«Сравнить адреса выхода» -- контурная', async () => {
    const root = await mount(<ExitCompareSection routerID={56} traffic={null} asleep={false} />)
    expect(root.querySelector('.compare-run').className).toContain('btn-ghost')
    cleanup(root)
  })

  it('тревога называет VPN-туннель именем, awgNN -- мелко', async () => {
    const { root } = await mountNow()
    const head = root.querySelector('.incident-card .incident-head')
    expect(head.textContent).toContain('VPN-туннель «vpn-nl» не отвечает')
    expect(head.querySelector('.data-row-code').textContent).toBe('awg10')
    cleanup(root)
  })

  it('упавший запасной: вердикт героя говорит, плитки «Запасного нет» нет', async () => {
    const { root } = await mountNow()
    expect(root.querySelector('.hero').textContent).toContain('vpn-nl')
    expect(root.textContent).not.toContain('Запасного VPN-туннеля нет')
    // Про упавший запасной говорит один вердикт -- шапка; плитка резерва не дублирует.
    expect(root.textContent).not.toContain('подхватить будет некому')
    cleanup(root)
  })

  it('две тревоги по VPN-туннелям -- лайм всё равно один', async () => {
    mocks.router.incidents.push({ check_name: 'tunnel_awg14', hard_since: SNAP.incidents[0].hard_since, fail_count: 3, acked: false })
    const { root } = await mountNow()
    expect(root.querySelectorAll('.incident-card')).toHaveLength(2)
    expect(limes(root)).toHaveLength(1)
    expect(buttons(root, 'Починить')).toHaveLength(2)
    cleanup(root)
  })
})

describe('«Сейчас» без тревог и без туннелей (Review Focus 1)', () => {
  it('порядок прежний, лаймов нет, плитка резерва не говорит «упал»', async () => {
    mocks.router.incidents = []
    mocks.checks = { checks: [], tunnels: [], traffic: null }
    const { root } = await mountNow()
    const o = order(root)
    expect(o[0]).toBe('hero')
    expect(o).not.toContain('alerts')
    expect(limes(root)).toHaveLength(0)
    expect(root.textContent).not.toContain('не отвечает')
    expect(root.textContent).toContain('Запасного VPN-туннеля нет')
    cleanup(root)
  })
})
