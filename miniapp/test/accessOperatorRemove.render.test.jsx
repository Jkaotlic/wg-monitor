// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Улучшение аудита 26.10: «✖» не отнимает доступ сразу -- сначала лист подтверждения.
const mocks = vi.hoisted(() => ({ access: null, removed: [] }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchAccess: () => Promise.resolve(structuredClone(mocks.access)),
  fetchPeople: () =>
    Promise.resolve([{ telegram_user_id: 2003, name: '', username: 'rook42', last_seen_at: null, is_admin: false, routers: [] }]),
  removeOperator: (routerID, id) => {
    mocks.removed.push([routerID, id])
    mocks.access.operators = mocks.access.operators.filter((o) => o.telegram_user_id !== id)
    return Promise.resolve(structuredClone(mocks.access))
  },
}))
const { AccessSection } = await import('../src/screens/AccessSection.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  mocks.access = { owner: { telegram_user_id: 2002 }, operators: [{ telegram_user_id: 2003 }] }
  mocks.removed = []
})

describe('удаление оператора', () => {
  it('«✖» открывает лист; доступ отнимается только по «Убрать»', async () => {
    const sheets = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<AccessSection routerID={7} openSheet={(s) => sheets.push(s)} />, root))
    await flush()
    await flush()
    await act(async () => root.querySelector('.access-operators button').click())
    expect(mocks.removed).toEqual([])
    expect(sheets).toHaveLength(1)
    expect(sheets[0]).toMatchObject({
      title: 'Убрать @rook42 из операторов?',
      body: 'Этот человек перестанет видеть роутер в приложении и получать тревоги о нём.',
      buttonLabel: 'Убрать',
      danger: true,
    })
    await act(async () => { await sheets[0].perform() })
    await flush()
    expect(mocks.removed).toEqual([[7, 2003]])
    expect(root.querySelector('.access-operators')).toBe(null)
    render(null, root)
    root.remove()
  })
})
