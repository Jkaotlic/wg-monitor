// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Имена вымышленные.
const mocks = vi.hoisted(() => ({ access: null, people: null, peopleCalls: 0, added: [], owners: [] }))
vi.mock('../src/api.js', async (importOriginal) => {
  const orig = await importOriginal()
  return {
    ...orig,
    fetchAccess: () => Promise.resolve(structuredClone(mocks.access)),
    fetchPeople: () => {
      mocks.peopleCalls++
      return mocks.people instanceof Error ? Promise.reject(mocks.people) : Promise.resolve(structuredClone(mocks.people))
    },
    addOperator: (routerID, id) => {
      mocks.added.push([routerID, id])
      mocks.access.operators.push({ telegram_user_id: id })
      return Promise.resolve(structuredClone(mocks.access))
    },
    setOwner: (routerID, owner) => {
      mocks.owners.push([routerID, owner])
      mocks.access.owner = { telegram_user_id: owner.telegram_user_id ?? 1 }
      return Promise.resolve(structuredClone(mocks.access))
    },
  }
})
const { AccessSection } = await import('../src/screens/AccessSection.jsx')
const { ApiError } = await import('../src/api.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AccessSection routerID={7} openSheet={null} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const rows = (root, scope) => [...root.querySelectorAll(`${scope} .person-pick`)]
const titles = (root, scope) => rows(root, scope).map((b) => b.querySelector('.person-title').textContent)
async function type(input, value) {
  await act(async () => {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function submit(form) {
  await act(async () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  await flush()
}

const PEOPLE = [
  { telegram_user_id: 2001, name: 'Ольга Новикова', username: 'olga_n', last_seen_at: new Date(Date.now() - 5 * 60_000).toISOString(), is_admin: false, routers: [] },
  { telegram_user_id: 2002, name: 'Пётр Сидоров', username: '', last_seen_at: null, is_admin: false, routers: [{ id: 7, nickname: 'дача', role: 'owner' }] },
  { telegram_user_id: 2003, name: '', username: 'rook42', last_seen_at: null, is_admin: false, routers: [{ id: 7, nickname: 'дача', role: 'operator' }] },
  { telegram_user_id: 2004, name: '<b>Жирный</b>', username: '', last_seen_at: null, is_admin: false, routers: [{ id: 9, nickname: 'офис', role: 'operator' }] },
]

beforeEach(() => {
  mocks.access = { owner: { telegram_user_id: 2002 }, operators: [{ telegram_user_id: 2003 }] }
  mocks.people = structuredClone(PEOPLE)
  mocks.peopleCalls = 0
  mocks.added = []
  mocks.owners = []
})

describe('v0.58: «Кому ещё открыт доступ» -- выбор из списка', () => {
  it('список людей: свободные первыми, уже имеющие доступ отмечены и не выбираются', async () => {
    const root = await mount()
    expect(titles(root, '.access-pick-operator')).toEqual(['Ольга Новикова (@olga_n)', '<b>Жирный</b>', 'Пётр Сидоров', '@rook42'])
    const [olga, , petr, rook] = rows(root, '.access-pick-operator')
    expect(olga.disabled).toBe(false)
    expect(olga.querySelector('.person-sub').textContent).toBe('ждёт доступа · писал боту 5 мин назад · номер 2001')
    expect(petr.disabled).toBe(true)
    expect(petr.textContent).toContain('уже владелец')
    expect(rook.disabled).toBe(true)
    expect(rook.textContent).toContain('уже оператор')
    cleanup(root)
  })

  it('имя -- текст, не разметка', async () => {
    const root = await mount()
    expect(root.querySelector('.access-pick-operator b')).toBe(null)
    cleanup(root)
  })

  it('поиск по имени, @нику, номеру и роутеру', async () => {
    const root = await mount()
    const search = root.querySelector('#access-find-operator')
    await type(search, 'новик')
    expect(titles(root, '.access-pick-operator')).toEqual(['Ольга Новикова (@olga_n)'])
    await type(search, '@rook')
    expect(titles(root, '.access-pick-operator')).toEqual(['@rook42'])
    await type(search, '2004')
    expect(titles(root, '.access-pick-operator')).toEqual(['<b>Жирный</b>'])
    await type(search, 'офис')
    expect(titles(root, '.access-pick-operator')).toEqual(['<b>Жирный</b>'])
    await type(search, 'никого-такого')
    expect(rows(root, '.access-pick-operator')).toHaveLength(0)
    expect(root.querySelector('.access-pick-operator').textContent).toContain('Никого не нашлось')
    cleanup(root)
  })

  it('«Добавить» без выбора не активна; выбрал -- уходит прежний запрос с номером', async () => {
    const root = await mount()
    const form = root.querySelector('form.access-pick-operator')
    const add = form.querySelector('button[type="submit"]')
    expect(add.textContent.trim()).toBe('Добавить')
    expect(add.disabled).toBe(true)
    await act(async () => rows(root, '.access-pick-operator')[0].click())
    expect(rows(root, '.access-pick-operator')[0].getAttribute('aria-pressed')).toBe('true')
    expect(add.disabled).toBe(false)
    await submit(form)
    expect(mocks.added).toEqual([[7, 2001]])
    // Добавленный -- уже оператор, повторно его не выбрать; в списке операторов -- по имени.
    const olga = rows(root, '.access-pick-operator').find((b) => b.textContent.includes('Ольга'))
    expect(olga.disabled).toBe(true)
    expect([...root.querySelectorAll('.access-operators .person-title')].map((s) => s.textContent)).toContain('Ольга Новикова (@olga_n)')
    // Справочник перечитан: роли в подписях свежие.
    expect(mocks.peopleCalls).toBe(2)
    cleanup(root)
  })

  it('ручной ввод номера -- свёрнутым запасным путём', async () => {
    const root = await mount()
    const manual = root.querySelector('details.access-manual-operator')
    expect(manual).toBeTruthy()
    expect(manual.open).toBe(false)
    expect(manual.querySelector('summary').textContent).toBe('Ввести номер вручную')
    await type(manual.querySelector('#access-add-operator'), '3005')
    await submit(manual.querySelector('form'))
    expect(mocks.added).toEqual([[7, 3005]])
    cleanup(root)
  })

  it('подсказка про /start вместо /myid', async () => {
    const root = await mount()
    expect(root.textContent).toContain('Человека нет в списке? Пусть откроет бота и нажмёт /start — он появится здесь по имени.')
    expect(root.textContent).not.toContain('/myid')
    cleanup(root)
  })
})

describe('v0.58: текущие владелец и операторы -- по имени', () => {
  it('имя и номер подписью', async () => {
    const root = await mount()
    const owner = root.querySelector('.access-owner')
    expect(owner.querySelector('.person-title').textContent).toBe('Пётр Сидоров')
    expect(owner.querySelector('.person-sub').textContent).toBe('номер 2002')
    const op = root.querySelector('.access-operators li')
    expect(op.querySelector('.person-title').textContent).toBe('@rook42')
    expect(op.querySelector('button').getAttribute('aria-label')).toBe('Удалить оператора @rook42')
    cleanup(root)
  })

  it('номера нет в справочнике -- голый номер, как раньше', async () => {
    mocks.access.operators = [{ telegram_user_id: 4444 }]
    const root = await mount()
    expect(root.querySelector('.access-operators .access-id').textContent).toBe('4444')
    cleanup(root)
  })
})

describe('v0.58: «Владелец» -- выбор из списка', () => {
  beforeEach(() => {
    mocks.access = { owner: null, operators: [{ telegram_user_id: 2003 }] }
  })

  it('оператор этого роутера владельцем выбирается; «Назначить» шлёт его номер', async () => {
    const root = await mount()
    const form = root.querySelector('form.access-pick-owner')
    const pick = rows(root, '.access-pick-owner').find((b) => b.textContent.includes('rook42'))
    expect(pick.disabled).toBe(false)
    await act(async () => pick.click())
    expect(form.querySelector('button[type="submit"]').textContent.trim()).toBe('Назначить')
    await submit(form)
    expect(mocks.owners).toEqual([[7, { telegram_user_id: 2003 }]])
    cleanup(root)
  })

  it('ручной ввод и «Назначить меня» остаются', async () => {
    const root = await mount()
    expect(root.querySelector('details.access-manual-owner #access-set-owner')).toBeTruthy()
    expect(button(root, 'Назначить меня владельцем')).toBeTruthy()
    cleanup(root)
  })
})

describe('v0.58: старый бэкенд без /people', () => {
  for (const [what, reply] of [
    ['404 -- null', null],
    ['ошибка запроса', new ApiError(500, 'unknown', '/people failed: 500')],
  ]) {
    it(`${what}: экран как раньше, с ручным вводом`, async () => {
      mocks.people = reply
      const root = await mount()
      expect(root.querySelector('.person-pick')).toBe(null)
      expect(root.querySelector('details.access-manual-operator')).toBe(null)
      const input = root.querySelector('#access-add-operator')
      expect(input).toBeTruthy()
      await type(input, '3006')
      await submit(input.closest('form'))
      expect(mocks.added).toEqual([[7, 3006]])
      expect(root.querySelector('.access-operators .access-id')).toBeTruthy()
      expect(root.textContent).not.toContain('/myid')
      expect(root.textContent).not.toContain('failed')
      cleanup(root)
    })
  }
})
