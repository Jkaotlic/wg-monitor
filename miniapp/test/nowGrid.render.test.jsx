// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { respond } from '../dev/fixtures.js'

const mocks = vi.hoisted(() => ({ router: null, checks: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')

async function mount(wide) {
  // Фикстура роутера 1 несёт настоящие по форме тревоги (INCIDENTS).
  mocks.router = structuredClone(respond('GET', '/v1/miniapp/routers/1'))
  mocks.checks = structuredClone(respond('GET', '/v1/miniapp/routers/1/events'))
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'web', wide }}>
        <RouterDetail id={1} isAdmin openSheet={() => {}} onTab={() => {}} onOpenAdmin={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return root
}

describe('«Сейчас» на широком экране', () => {
  it('две колонки: слева схема и плитки, справа тревоги и действия', async () => {
    const root = await mount(true)
    const main = root.querySelector('.now-grid > .now-main')
    const side = root.querySelector('.now-grid > .now-side')
    expect(main.querySelector('.hero')).toBeTruthy()
    expect(main.querySelector('.stat-grid')).toBeTruthy()
    expect(side.textContent).toContain('Активные тревоги')
    expect(side.textContent).toContain('Быстрые действия')
    expect(side.textContent).toContain('Администрирование')
    expect(main.textContent).not.toContain('Быстрые действия')
    // Имя роутера -- в шапке основной области, в герое не дублируется.
    expect(root.querySelector('.hero h1')).toBe(null)
    render(null, root)
    root.remove()
  })

  it('телефон: прежняя одна колонка, имя в герое', async () => {
    const root = await mount(false)
    expect(root.querySelector('.now-grid')).toBe(null)
    expect(root.querySelector('.hero h1').textContent).toBe(mocks.router.router.nickname)
    const titles = [...root.querySelectorAll('.section-title')].map((n) => n.textContent)
    expect(titles.indexOf('Активные тревоги')).toBeLessThan(titles.indexOf('Быстрые действия'))
    render(null, root)
    root.remove()
  })
})
