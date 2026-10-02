// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import SNAP from './fixtures/split_reserve_dead.json'

const mocks = vi.hoisted(() => ({ versions: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve({ router: structuredClone(SNAP.router), incidents: structuredClone(SNAP.incidents) }),
  fetchRouterChecks: () => Promise.resolve(structuredClone(SNAP.events)),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))
const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(props = {}, wide = false) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide }}>
        <RouterDetail id={56} openSheet={() => {}} onTab={() => {}} openLayer={() => {}} {...props} />
      </AppContext.Provider>,
      root,
    ),
  )
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const buttonTexts = (root) => [...root.querySelectorAll('button')].map((b) => b.textContent.trim())

beforeEach(() => {
  mocks.versions = null
})

describe('v0.52: «Роутер» -- у каждого действия один дом', () => {
  for (const wide of [false, true]) {
    it(`нет дублей вкладок (wide=${wide})`, async () => {
      const root = await mount({}, wide)
      expect(root.textContent).not.toContain('Быстрые действия')
      expect(root.textContent).not.toContain('Собрать диагностику')
      expect(root.querySelector('.compare-run')).toBe(null)
      expect(root.querySelector('details.checks-spoiler')).toBe(null)
      // Исключение спеки: в беде короткий путь остаётся на карточке тревоги.
      expect(buttonTexts(root)).toContain('Починить')
      expect(buttonTexts(root)).toContain('Перезапустить VPN-туннель')
      expect(root.querySelectorAll('.btn-primary')).toHaveLength(1)
      cleanup(root)
    })
  }

  it('плашка обслуживания ведёт в «Настройки» → «Обслуживание»', async () => {
    mocks.versions = { rows: [], unknown: [], reboot_hint: 'kmod' }
    const onOpenService = vi.fn()
    const root = await mount({ onOpenService })
    const card = root.querySelector('.maint-notice')
    expect(card.textContent).toContain('Открыть «Настройки» → «Обслуживание»')
    await act(async () => card.click())
    expect(onOpenService).toHaveBeenCalledTimes(1)
    cleanup(root)
  })

  it('тревога на другом роутере -- строка с переходом', async () => {
    const onOpenRouter = vi.fn()
    const root = await mount({ otherAlert: { id: 3, nickname: 'дача-северная' }, onOpenRouter })
    const line = root.querySelector('.other-alert')
    expect(line.textContent).toBe('На «дача-северная» тревога — открыть')
    await act(async () => line.click())
    expect(onOpenRouter).toHaveBeenCalledWith(3)
    cleanup(root)
  })

  it('без тревоги на другом -- строки нет', async () => {
    const root = await mount({ otherAlert: null })
    expect(root.querySelector('.other-alert')).toBe(null)
    cleanup(root)
  })
})
