// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ accounts: [], role: 'owner', settingsFail: false, job: null }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchReplaceStatus: () => Promise.resolve(mocks.job),
    fetchVPNAccounts: () => Promise.resolve({ accounts: structuredClone(mocks.accounts) }),
    fetchRouterSettings: () => (mocks.settingsFail ? Promise.reject(new Error('net')) : Promise.resolve({ role: mocks.role })),
    startReplace: () => Promise.resolve({ steps: [] }),
  }
})

const { ReplaceScreen } = await import('../src/screens/ReplaceScreen.jsx')

const FULL = {
  provider: 'amnezia',
  label: 'Amnezia Premium',
  connected: true,
  devices_used: 3,
  devices_max: 3,
  options: [
    { id: 'nl', label: 'Нидерланды', issued: true },
    { id: 'de', label: 'Германия' },
  ],
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const seen = { closed: 0, cabinet: 0 }
  await act(async () =>
    render(
      <ReplaceScreen
        routerID={7}
        tunnel={{ id: 'Wireguard0', name: 'старый' }}
        policyName="VPN"
        onClose={() => seen.closed++}
        onOpenCabinet={() => seen.cabinet++}
        {...props}
      />,
      root,
    ),
  )
  await flush()
  await flush()
  return { root, seen }
}

beforeEach(() => {
  mocks.accounts = [structuredClone(FULL)]
  mocks.role = 'owner'
  mocks.settingsFail = false
  mocks.job = null
})

describe('кабинеты в мастере замены', () => {
  it('не предлагает API Premium даже при доступных странах на старом сервере', async () => {
    const onOpenCabinet = vi.fn()
    const { root } = await mount({ onOpenCabinet })
    expect(root.querySelector('.list-row')).toBeNull()
    await act(async () => button(root, 'Кабинет Amnezia Premium').click())
    expect(onOpenCabinet).toHaveBeenCalledWith('amnezia')
    render(null, root)
  })

  it('оставляет выпуск HideMy.name через сервер', async () => {
    mocks.accounts = [{ provider: 'hidemyname', label: 'HideMy.name', connected: true, options: [{ id: 'de', label: 'Германия' }] }]
    const { root } = await mount()
    expect(root.querySelector('.list-row .row-title').textContent).toBe('Германия')
    render(null, root)
  })
})

// Мастер оставляет VPN-туннель на роутере всегда; разбираться с ним человек
// идёт на экран этого VPN-туннеля.
describe('мастер замены: что осталось на роутере', () => {
  it('успех -- кнопка к экрану прежнего VPN-туннеля', async () => {
    mocks.job = { job_id: 'j1', state: 'success', steps: [], hint: 'готово: общий набор «VPN» идёт через «новый»' }
    const opened = []
    let done = 0
    const { root } = await mount({ onOpenTunnel: (id) => opened.push(id), onDone: () => done++ })
    expect(root.querySelector('.replace-leftover').textContent).toContain('Прежний VPN-туннель «старый» остался на роутере выключенным')
    await act(async () => button(root, 'К VPN-туннелю «старый»').click())
    expect(opened).toEqual(['Wireguard0'])
    expect(done).toBe(1)
    render(null, root)
  })

  it('откат оставил новый VPN-туннель -- к списку', async () => {
    mocks.job = {
      job_id: 'j1',
      state: 'failed',
      steps: [],
      hint: 'новый VPN-туннель не обменялся ключами. Откат: новый VPN-туннель выключен и оставлен на роутере',
    }
    const opened = []
    const { root } = await mount({ onOpenTunnel: (id) => opened.push(id) })
    await act(async () => button(root, 'К списку VPN-туннелей').click())
    expect(opened).toEqual([null])
    render(null, root)
  })

  it('без перехода с вкладки -- карточки нет', async () => {
    mocks.job = { job_id: 'j1', state: 'success', steps: [], hint: 'готово' }
    const { root } = await mount()
    expect(root.querySelector('.replace-leftover')).toBe(null)
    render(null, root)
  })
})
