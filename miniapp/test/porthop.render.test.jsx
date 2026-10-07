// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Экран «Смена порта при блокировке» целиком: гейт версии и роли, статус на
// входе (не у спящего роутера), имена VPN-туннелей из снимка, ручная копия и
// её замена, журнал.
const mocks = vi.hoisted(() => ({ settings: null, calls: [], answers: {} }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
}))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action, args, opts) => {
          mocks.calls.push({ action, args, deadlineMs: opts?.deadlineMs })
          const pick = mocks.answers[action]
          const res = typeof pick === 'function' ? pick(args) : (pick ?? null)
          setState({ busy: false, result: res, error: null, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { PorthopScreen } = await import('../src/screens/PorthopScreen.jsx')
const { rememberRouteSnapshot } = await import('../src/routes.js')

const BASE = {
  installed: false,
  running: false,
  auto: true,
  watched: ['opkgtun10', 'opkgtun12'],
  legacy: { found: false, running: false },
  hops_24h: 0,
  recovered_24h: 0,
  failed_24h: 0,
  script_path: '/opt/etc/wg-monitor/awg-porthop.sh',
  conf_path: '/opt/etc/wg-monitor/porthop.conf',
  log_path: '/opt/var/log/wg-monitor/porthop.log',
}
const ON = { ...BASE, installed: true, running: true, hops_24h: 2, recovered_24h: 2, last_event: '2026-10-07 12:00:00 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)' }
const LEGACY = { found: true, path: '/opt/etc/init.d/S99awg-porthop', running: true }
const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<PorthopScreen routerID={2} routerName="home" asleep={false} onClose={() => {}} {...props} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const click = async (root, text) => {
  await act(async () => button(root, text).click())
  await flush()
}

beforeEach(() => {
  mocks.settings = { role: 'admin', agent_version: 'v0.57.0' }
  mocks.calls = []
  mocks.answers = {
    porthop_status: ok(BASE),
    porthop_install: (args) => (args.replace_legacy ? ok({ ...ON, legacy: { found: false, path: LEGACY.path, running: false, moved_to: '/opt/etc/wg-monitor/legacy/S99awg-porthop' } }) : ok(ON)),
    porthop_remove: ok(BASE),
    porthop_logs: ok({ ...ON, log_tail: 'строка 1\nстрока 2' }),
  }
})

describe('«Смена порта при блокировке»', () => {
  it('объясняет простыми словами, что делает', async () => {
    const root = await mount()
    expect(root.querySelector('h1').textContent).toBe('Смена порта при блокировке')
    expect(root.textContent).toContain('роутер сам меняет свой исходящий порт')
    expect(root.textContent).toContain('VPN-туннель снова поднимается')
    expect(root.textContent).toContain('через которые идёт весь трафик')
    cleanup(root)
  })

  it('старому агенту -- ни одной кнопки и ни одной команды', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.56.0' }
    const root = await mount()
    expect(root.textContent).toContain('появится после обновления агента на роутере до v0.57')
    expect(root.textContent).toContain('v0.56.0')
    expect(button(root, 'Включить')).toBeUndefined()
    expect(button(root, 'Проверить')).toBeUndefined()
    expect(mocks.calls).toEqual([])
    cleanup(root)
  })

  it('не админу -- слова, а не кнопки', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.57.0' }
    const root = await mount()
    expect(root.textContent).toContain('включает админ бота')
    expect(mocks.calls).toEqual([])
    cleanup(root)
  })

  it('на входе спрашивает состояние; спящему роутеру -- нет', async () => {
    let root = await mount()
    expect(mocks.calls.map((c) => [c.action, c.args])).toEqual([['porthop_status', {}]])
    cleanup(root)
    mocks.calls = []
    root = await mount({ asleep: true })
    expect(mocks.calls).toEqual([])
    expect(root.textContent).toContain('Состояние ещё не проверено.')
    cleanup(root)
  })

  it('выключено: что будет сторожить -- именами VPN-туннелей из снимка; «Включить» ставит без списка', async () => {
    rememberRouteSnapshot(2, { tunnels: [{ id: 'awg10', name: 'Нидерланды', iface: 'opkgtun10' }] })
    const root = await mount()
    expect(root.textContent).toContain('выключено')
    expect(root.textContent).toContain('Будет сторожить')
    expect(root.textContent).toContain('Нидерланды, opkgtun12')
    expect(button(root, 'Выключить')).toBeUndefined()
    await click(root, 'Включить')
    expect(mocks.calls.at(-1)).toMatchObject({ action: 'porthop_install', args: {} })
    expect(root.textContent).toContain('Смена порта включена.')
    expect(root.textContent).toContain('включено, работает')
    expect(root.textContent).toContain('2 смены порта: ожили 2, не ожили 0')
    expect(root.textContent).toContain('поток ожил')
    expect(button(root, 'Включить')).toBeUndefined()
    await click(root, 'Выключить')
    expect(mocks.calls.at(-1).action).toBe('porthop_remove')
    expect(root.textContent).toContain('Смена порта выключена.')
    cleanup(root)
  })

  it('ручная копия: предупреждение и замена с replace_legacy, затем -- куда она перенесена', async () => {
    mocks.answers.porthop_status = ok({ ...BASE, legacy: LEGACY })
    const root = await mount()
    expect(root.textContent).toContain('Ручная копия')
    expect(button(root, 'Включить')).toBeUndefined()
    await click(root, 'Заменить ручную копию')
    expect(mocks.calls.at(-1)).toMatchObject({ action: 'porthop_install', args: { replace_legacy: true } })
    expect(root.textContent).toContain('Ручная копия перенесена в /opt/etc/wg-monitor/legacy/S99awg-porthop')
    expect(root.textContent).toContain('вернуть — перенести файл обратно в /opt/etc/init.d')
    expect(button(root, 'Заменить ручную копию')).toBeUndefined()
    cleanup(root)
  })

  it('отказ legacy_running на «Включить» -- человеческий текст и кнопка замены, без сырого вывода', async () => {
    mocks.answers.porthop_install = (args) =>
      args.replace_legacy
        ? ok(ON)
        : { status: 'err', output: 'legacy_running: на роутере есть ручная копия смены порта (/opt/etc/init.d/S99awg-porthop) — две копии дрались бы за один VPN-туннель.' }
    const root = await mount()
    await click(root, 'Включить')
    expect(root.textContent).toContain('На роутере уже стоит ручная копия смены порта (/opt/etc/init.d/S99awg-porthop)')
    expect(root.textContent).not.toContain('legacy_running')
    expect(button(root, 'Заменить ручную копию')).toBeDefined()
    await click(root, 'Заменить ручную копию')
    expect(mocks.calls.at(-1).args).toEqual({ replace_legacy: true })
    expect(root.textContent).toContain('Смена порта включена.')
    cleanup(root)
  })

  it('«Журнал» показывает хвост журнала', async () => {
    const root = await mount()
    await click(root, 'Журнал')
    expect(mocks.calls.at(-1)).toMatchObject({ action: 'porthop_logs', args: { lines: 100 } })
    expect(root.querySelector('.packages-log').textContent).toBe('строка 1\nстрока 2')
    cleanup(root)
  })
})
