// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ calls: [], answers: {} }))

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

const { PackagesScreen } = await import('../src/screens/PackagesScreen.jsx')
const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')
const { AGENT_OLDER_THAN_APP } = await import('../src/labels.js')

const OPKG = { installed: true, schedule: '30 4 * * *', script_path: '/opt/bin/a', log_path: '/opt/var/log/a', free_kb: 512000, min_free_kb: 102400, last_run: '2026-09-17T01:30:00Z', last_status: 'ok', log_tail: '2026-09-17T01:30:00Z status=ok' }
const CLEAN = { installed: false, script_path: '/opt/bin/b', log_path: '/opt/var/log/b', mem_available_kb: 60000 }
const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })
const cronOf = (hhmm) => {
  const [h, m] = hhmm.split(':')
  return `${Number(m)} ${Number(h)} * * *`
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<PackagesScreen routerID={2} routerName="home" asleep={false} onClose={() => {}} {...props} />, root))
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }
const card = (root, kind) => root.querySelector(`.packages-card-${kind}`)
const button = (el, text) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)
async function setTime(el, value) {
  const input = el.querySelector('input[type="time"]')
  await act(async () => {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

beforeEach(() => {
  mocks.calls = []
  mocks.answers = {
    opkg_cron_status: ok(OPKG),
    entware_clean_status: ok(CLEAN),
    opkg_cron_install: (args) => ok({ ...OPKG, schedule: cronOf(args.schedule) }),
    opkg_cron_logs: ok({ ...OPKG, log_tail: 'строка 1\nстрока 2' }),
    opkg_cron_remove: ok({ ...OPKG, installed: false, schedule: '' }),
    entware_clean_run: ok({ ...CLEAN, last_freed_kb: 2048 }),
  }
})

describe('«Пакеты по расписанию»', () => {
  it('на входе спрашивает оба состояния и рисует их словами', async () => {
    const root = await mount()
    expect(mocks.calls.map((c) => [c.action, c.args])).toEqual([
      ['opkg_cron_status', { lines: 40 }],
      ['entware_clean_status', { lines: 40 }],
    ])
    const opkg = card(root, 'opkg')
    expect(opkg.textContent).toContain('Обновление пакетов по расписанию')
    expect(opkg.textContent).toContain('каждый день в 04:30')
    expect(opkg.querySelector('input[type="time"]').value).toBe('04:30')
    expect(opkg.querySelector('input[type="time"]').getAttribute('autocomplete')).toBe('off')
    expect(button(opkg, 'Изменить время')).toBeTruthy()
    expect(button(opkg, 'Выключить')).toBeTruthy()
    expect(button(opkg, 'Запустить сейчас')).toBeUndefined()
    const clean = card(root, 'clean')
    expect(clean.textContent).toContain('Очистка Entware')
    expect(clean.textContent).toContain('выключено')
    expect(clean.querySelector('input[type="time"]').value).toBe('05:15')
    expect(button(clean, 'Включить')).toBeTruthy()
    expect(button(clean, 'Выключить')).toBeUndefined()
    expect(button(clean, 'Запустить сейчас')).toBeTruthy()
    cleanup(root)
  })

  it('смена времени -- установка с HH:MM и итог словами', async () => {
    const root = await mount()
    const opkg = card(root, 'opkg')
    await setTime(opkg, '03:15')
    await act(async () => button(opkg, 'Изменить время').click())
    await flush()
    const call = mocks.calls.find((c) => c.action === 'opkg_cron_install')
    expect(call.args).toEqual({ schedule: '03:15' })
    expect(call.deadlineMs).toBe(6 * 60_000)
    expect(opkg.textContent).toContain('Расписание сохранено: каждый день в 03:15.')
    expect(opkg.textContent).toContain('каждый день в 03:15')
    cleanup(root)
  })

  it('негодное время -- кнопка погашена и сказано почему', async () => {
    const root = await mount()
    const opkg = card(root, 'opkg')
    await setTime(opkg, '')
    expect(button(opkg, 'Изменить время').disabled).toBe(true)
    expect(opkg.textContent).toContain('Время пишется так: 04:30.')
    cleanup(root)
  })

  it('журнал -- 100 строк моноширинным блоком', async () => {
    const root = await mount()
    const opkg = card(root, 'opkg')
    await act(async () => button(opkg, 'Журнал').click())
    await flush()
    expect(mocks.calls.find((c) => c.action === 'opkg_cron_logs').args).toEqual({ lines: 100 })
    expect(opkg.querySelector('pre.packages-log').textContent).toBe('строка 1\nстрока 2')
    cleanup(root)
  })

  it('выключить и запустить очистку сейчас', async () => {
    const root = await mount()
    await act(async () => button(card(root, 'opkg'), 'Выключить').click())
    await flush()
    expect(card(root, 'opkg').textContent).toContain('Расписание снято.')
    expect(button(card(root, 'opkg'), 'Выключить')).toBeUndefined()
    await act(async () => button(card(root, 'clean'), 'Запустить сейчас').click())
    await flush()
    expect(mocks.calls.find((c) => c.action === 'entware_clean_run').args).toEqual({})
    expect(card(root, 'clean').textContent).toContain('Очистка выполнена, освобождено 2 МБ.')
    cleanup(root)
  })

  it('спящий роутер -- на входе ничего не шлём', async () => {
    const root = await mount({ asleep: true })
    expect(mocks.calls).toEqual([])
    expect(card(root, 'opkg').textContent).toContain('Состояние ещё не проверено.')
    expect(root.textContent).toContain('Роутер сейчас не на связи')
    cleanup(root)
  })

  it('старый агент -- своей фразой; ошибка -- подробности под раскрытием', async () => {
    mocks.answers.opkg_cron_status = { status: 'err', output: 'unknown action: opkg_cron_status' }
    mocks.answers.entware_clean_status = { status: 'err', output: 'exec not configured' }
    const root = await mount()
    expect(card(root, 'opkg').textContent).toContain(AGENT_OLDER_THAN_APP)
    const clean = card(root, 'clean')
    expect(clean.textContent).toContain('Роутер ответил ошибкой — подробности ниже.')
    expect(clean.querySelector('details.packages-details pre').textContent).toBe('exec not configured')
    cleanup(root)
  })
})

describe('проводка экрана', () => {
  it('не-админу по адресу open=packages -- слова, а не экран', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    const nav = { routerID: 2, tab: 'router', overlay: 'packages', sheet: null }
    await act(async () => render(<OverlayHost nav={nav} dispatch={() => {}} routers={[{ id: 2, nickname: 'home', status: 'online' }]} isAdmin={false} />, root))
    await flush()
    expect(root.textContent).toContain('Этот экран доступен только администратору.')
    expect(mocks.calls).toEqual([])
    cleanup(root)
  })

  it('оверлей packages открывает экран, «назад» ведёт во вкладку «Управление»', async () => {
    const dispatch = vi.fn()
    const root = document.createElement('div')
    document.body.appendChild(root)
    const nav = { routerID: 2, tab: 'router', overlay: 'packages', sheet: null }
    await act(async () => render(<OverlayHost nav={nav} dispatch={dispatch} routers={[{ id: 2, nickname: 'home', status: 'online' }]} isAdmin />, root))
    await flush()
    expect(root.querySelector('.overlay-title').textContent).toBe('Пакеты по расписанию')
    await act(async () => root.querySelector('.overlay-back').click())
    expect(dispatch).toHaveBeenCalledWith({ type: 'overlay', overlay: 'manage' })
    cleanup(root)
  })
})
