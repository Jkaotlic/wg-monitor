// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ versions: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve({ router: { id: 7, nickname: 'r', status: 'online', last_seen_age_sec: 20 }, incidents: [] }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [], traffic: null }),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))
const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')

async function mount(onTab) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <RouterDetail id={7} openSheet={() => {}} onTab={onTab} />
      </AppContext.Provider>,
      root,
    ),
  )
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return root
}

describe('подсветка обслуживания на «Сейчас»', () => {
  it('перезагрузка видна и ведёт в «Управление»', async () => {
    mocks.versions = { rows: [], unknown: [], reboot_hint: 'kmod', agent: { installed: 'v0.41.0', available: 'v0.43.0' } }
    const onTab = vi.fn()
    const root = await mount(onTab)
    const card = root.querySelector('.maint-notice')
    expect(card.textContent).toContain('Нужна перезагрузка роутера')
    expect(card.textContent).toContain('Агент wg-monitor: v0.43.0 (сейчас v0.41.0)')
    await act(async () => card.click())
    expect(onTab).toHaveBeenCalledWith('manage')
    render(null, root)
    root.remove()
  })
  it('нечего сказать -- карточки нет', async () => {
    mocks.versions = { rows: [], unknown: [] }
    const root = await mount(() => {})
    expect(root.querySelector('.maint-notice')).toBe(null)
    render(null, root)
    root.remove()
  })
})
