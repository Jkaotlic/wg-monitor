// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ routers: { routers: [] }, calls: 0 }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchRouters: () => {
      mocks.calls++
      return Promise.resolve(mocks.routers)
    },
  }
})

const { NoAccess } = await import('../src/screens/NoAccess.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }

describe('NoAccess', () => {
  it('называет Telegram ID из сессии и даёт его скопировать', async () => {
    const root = await mount(<NoAccess telegramUserID={777001} />)
    const text = root.textContent
    expect(text).toContain('Передайте администратору ваш Telegram ID')
    expect(root.querySelector('.noaccess-id-value').textContent).toBe('777001')
    expect(root.querySelector('.copy button').textContent).toBe('Скопировать')
    // Темы группы больше нет -- и звать в неё нельзя.
    expect(text).not.toContain('теме')
    expect(text).not.toContain('бота')
    cleanup(root)
  })

  it('без номера в сессии просит назвать себя, а не показывает ноль', async () => {
    const root = await mount(<NoAccess telegramUserID={0} />)
    expect(root.textContent).toContain('своё имя в Telegram')
    expect(root.textContent).not.toContain('Telegram ID: 0')
    expect(root.querySelector('.copy')).toBe(null)
    cleanup(root)
  })

  it('«Проверить снова» переспрашивает сервер и отдаёт список, когда доступ появился', async () => {
    mocks.calls = 0
    mocks.routers = { routers: [{ id: 2, nickname: 'Дача' }] }
    const got = []
    const root = await mount(<NoAccess telegramUserID={777001} onRetry={(list) => got.push(list)} />)
    await act(async () => root.querySelector('.btn-primary').click())
    await flush()
    expect(mocks.calls).toBe(1)
    expect(got).toHaveLength(1)
    expect(got[0][0].nickname).toBe('Дача')
    cleanup(root)
  })

  it('доступа по-прежнему нет -- честная строка вместо молчания', async () => {
    mocks.calls = 0
    mocks.routers = { routers: [] }
    const root = await mount(<NoAccess telegramUserID={777001} onRetry={() => { throw new Error('доступа нет, звать не должны') }} />)
    await act(async () => root.querySelector('.btn-primary').click())
    await flush()
    expect(root.querySelector('.hint').textContent).toContain('Пока ничего не изменилось')
    cleanup(root)
  })
})
