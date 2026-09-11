// @vitest-environment jsdom
import { describe, it, expect, beforeAll, vi } from 'vitest'
import { render, options } from 'preact'
import { act } from 'preact/test-utils'
import { installKeepTogether } from '../src/text.js'
import { ROUTERS, respond } from '../dev/fixtures.js'

// Настоящие экраны с хуком склейки, как в main.jsx: главный экран роутера и
// «Проверки». Имя VPN-туннеля с дефисом («vpn-nl») приходит в готовых строках
// (routerHeadline, parseDiag) и обязано лечь в .q с настоящим дефисом --
// иначе оно рвётся переносом «vpn-» / «nl».
const NBH = '‑'

const mocks = vi.hoisted(() => ({ router: null, checks: null, command: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({
    busy: false,
    result: mocks.command,
    error: null,
    errorCode: null,
    run: () => Promise.resolve(mocks.command),
  }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { DiagTab } = await import('../src/screens/DiagTab.jsx')

beforeAll(() => installKeepTogether(options))

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(vnode, root)
  })
  // Экраны читают данные в эффекте: дать промисам разрешиться.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return root
}

function unmount(root) {
  render(null, root)
  root.remove()
}

// Имя с дефисом, лежащее голым текстом, а не в .q, -- то, что может
// разорваться по дефису.
function unprotectedNames(root) {
  const found = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let n
  while ((n = walker.nextNode())) {
    if (n.parentElement.closest('.q, pre, .raw-dump')) continue
    found.push(...(n.data.match(/«[^«»]*[-‑][^«»]*»/g) ?? []))
  }
  return found
}

// Снимок роутера из фикстур разработки, VPN-туннели переименованы в имена с
// дефисом.
function routerData() {
  const checks = structuredClone(respond('GET', '/v1/miniapp/routers/2/events'))
  const names = { awg12: 'vpn-nl', awg10: 'vpn-de' }
  for (const t of checks.tunnels) if (names[t.tunnel_id]) t.name = names[t.tunnel_id]
  checks.traffic.egress_tunnel_name = 'vpn-nl'
  return { router: { router: ROUTERS[1], incidents: [] }, checks }
}

describe('главный экран роутера', () => {
  it('имя в вердикте шапки и в строке запасного -- в .q, с обычным дефисом', async () => {
    const data = routerData()
    mocks.router = data.router
    mocks.checks = data.checks
    mocks.command = null
    const root = await mount(<RouterDetail id={2} openSheet={() => {}} onTab={() => {}} />)

    const verdict = root.querySelector('.hero .traffic-detail')
    expect(verdict.textContent).toBe('Заблокированное открывается через «vpn-nl». Остальное идёт напрямую, как обычно.')
    expect([...verdict.querySelectorAll('.q')].map((q) => q.textContent)).toEqual(['«vpn-nl»'])

    const backup = root.querySelector('.row-note')
    expect(backup.textContent).toBe('«vpn-de» подхватит, если этот замолчит')
    expect(backup.querySelector('.q').textContent).toBe('«vpn-de»')

    expect(unprotectedNames(root)).toEqual([])
    expect([...root.querySelectorAll('.q')].filter((q) => q.textContent.includes(NBH))).toEqual([])
    unmount(root)
  })
})

describe('«Проверки»', () => {
  it('строка карточки отчёта «VPN-туннель «vpn-de»: …» -- имя в .q, термин склеен', async () => {
    mocks.router = { router: ROUTERS[1] }
    mocks.checks = { checks: [], tunnels: [] }
    mocks.command = {
      status: 'ok',
      output: JSON.stringify({
        version: '1.0',
        generatedAt: '2026-08-18T09:00:00Z',
        durationMs: 16416,
        system: { appVersion: '2.18.2+r1', kernelModule: { exists: true, loaded: true } },
        wan: { anyUp: true, interfaces: { eth3: { up: true, label: 'Провайдер' } } },
        tests: [
          { name: 'wan_connectivity', description: '', status: 'pass', detail: '' },
          {
            name: 'awg_handshake',
            description: '',
            status: 'fail',
            detail: 'рукопожатия нет 90 минут',
            tunnelId: 'awg10',
            tunnelName: 'vpn-de',
          },
        ],
      }),
    }
    const root = await mount(<DiagTab routerID={2} />)

    const card = [...root.querySelectorAll('.diag-card')].find((c) => c.textContent.includes('Обмен ключами'))
    const detail = card.querySelector('.tunnel-sub')
    expect(detail.textContent).toBe(`VPN${NBH}туннель «vpn-de»: рукопожатия нет 90 минут`)
    expect(detail.querySelector('.q').textContent).toBe('«vpn-de»')

    expect(unprotectedNames(root)).toEqual([])
    unmount(root)
  })
})
