// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Имена вымышленные. «Кто может выпускать конфиги» на экране панели VPS:
// тот же выбор человека из списка, что на экране доступа (оператор 08.10:
// «щелкаю по пустому полю и ничего»).
const mocks = vi.hoisted(() => ({ people: null, peopleCalls: 0, added: [], removed: [] }))
vi.mock('../src/api.js', async (importOriginal) => {
  const orig = await importOriginal()
  return {
    ...orig,
    fetchPeople: () => {
      mocks.peopleCalls++
      return mocks.people instanceof Error ? Promise.reject(mocks.people) : Promise.resolve(structuredClone(mocks.people))
    },
    addAwg3Issuer: (panelID, id) => {
      mocks.added.push([panelID, id])
      return Promise.resolve({ panel: { id: panelID, issuers: [{ telegram_user_id: 300 }, { telegram_user_id: id }] } })
    },
    removeAwg3Issuer: (panelID, id) => {
      mocks.removed.push([panelID, id])
      return Promise.resolve({ panel: { id: panelID, issuers: [] } })
    },
  }
})
const { Awg3Issuers } = await import('../src/screens/Awg3Issuers.jsx')

const PEOPLE = [
  { telegram_user_id: 400, name: 'Ольга Новикова', username: 'olga_nov', last_seen_at: null, is_admin: false, routers: [] },
  { telegram_user_id: 300, name: 'Пётр Сидоров', username: '', last_seen_at: null, is_admin: false, routers: [{ id: 1, nickname: 'router-a', role: 'owner' }] },
]

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(panel) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  let changed = null
  const onChanged = (p) => { changed = p }
  await act(async () => render(<Awg3Issuers panel={panel} onChanged={onChanged} />, root))
  await flush()
  await flush()
  return { root, changed: () => changed }
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const rows = (root) => [...root.querySelectorAll('.person-pick')]
async function click(el) {
  await act(async () => el.click())
  await flush()
}

describe('Awg3Issuers: выбор человека из списка', () => {
  beforeEach(() => {
    mocks.people = structuredClone(PEOPLE)
    mocks.peopleCalls = 0
    mocks.added = []
    mocks.removed = []
  })

  it('список людей виден сразу, выбор отправляет номер выбранного', async () => {
    const { root } = await mount({ id: 'p1', issuers: [{ telegram_user_id: 300 }] })
    const olga = rows(root).find((b) => b.textContent.includes('Ольга Новикова'))
    expect(olga).toBeTruthy()
    const add = button(root, 'Добавить')
    expect(add.disabled).toBe(true)
    await click(olga)
    expect(button(root, 'Добавить').disabled).toBe(false)
    await click(button(root, 'Добавить'))
    expect(mocks.added).toEqual([['p1', 400]])
    expect(mocks.peopleCalls).toBe(2) // список перечитан после правки
    cleanup(root)
  })

  it('уже выпускающий отмечен и не выбирается; в списке выпускающих -- имя', async () => {
    const { root } = await mount({ id: 'p1', issuers: [{ telegram_user_id: 300 }] })
    const petr = rows(root).find((b) => b.textContent.includes('Пётр Сидоров'))
    expect(petr.disabled).toBe(true)
    expect(petr.textContent).toContain('уже выпускает')
    const listed = root.querySelector('.awg3-issuers-list')
    expect(listed.textContent).toContain('Пётр Сидоров')
    expect(listed.textContent).toContain('номер 300')
    cleanup(root)
  })

  it('поиск: «петр» находит «Пётр»', async () => {
    const { root } = await mount({ id: 'p1', issuers: [] })
    const input = root.querySelector('#awg3-issuer-pick')
    await act(async () => {
      input.value = 'петр'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await flush()
    expect(rows(root).map((b) => b.querySelector('.person-title').textContent)).toEqual(['Пётр Сидоров'])
    cleanup(root)
  })

  it('старый бэкенд без /people -- прежнее поле номера, ввод работает', async () => {
    mocks.people = null
    const { root } = await mount({ id: 'p1', issuers: [] })
    expect(rows(root).length).toBe(0)
    const input = root.querySelector('#awg3-issuer-id')
    expect(input).toBeTruthy()
    expect(input.closest('details')).toBeNull() // не спрятано: другого пути нет
    await act(async () => {
      input.value = '555'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await click(button(root, 'Добавить'))
    expect(mocks.added).toEqual([['p1', 555]])
    cleanup(root)
  })

  it('со списком ручной ввод -- свёрнутый запасной путь', async () => {
    const { root } = await mount({ id: 'p1', issuers: [] })
    const manual = root.querySelector('#awg3-issuer-id')
    expect(manual.closest('details')).toBeTruthy()
    cleanup(root)
  })
})
