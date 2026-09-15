// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({
  fleet: null, fleetCalls: 0, updates: [], cancels: [], fleetUpdates: [],
  updateReply: null, cancelReply: null, fleetReply: null,
  sent: [], results: {}, resultResolvers: {}, notify: [], notifyReply: null, notifyReplies: {},
  // Первое чтение (на монтировании) всегда успевает -- иначе экран никогда
  // не покажет ни одной строки. Отказ этот флаг включает начиная со второго.
  fleetFailAfterFirst: false,
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    fetchFleet: () => {
      mocks.fleetCalls++
      if (mocks.fleetFailAfterFirst && mocks.fleetCalls > 1) return Promise.reject(new Error('boom'))
      return Promise.resolve(mocks.fleet)
    },
    fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
    createWebLink: () => Promise.resolve({ url: 'https://wg.example.com/x', notice: '', limit_notice: '' }),
    updateRouterAgent: (id, confirm, target) => {
      mocks.updates.push({ id, confirm, target })
      return reply(mocks.updateReply)
    },
    cancelRouterAgentUpdate: (id) => {
      mocks.cancels.push(id)
      return reply(mocks.cancelReply)
    },
    updateFleetAgents: (confirm) => {
      mocks.fleetUpdates.push(confirm)
      return reply(mocks.fleetReply)
    },
    sendCommand: (routerID, action, args) => {
      mocks.sent.push({ routerID, action, args })
      return Promise.resolve({ cmd_id: `c${routerID}` })
    },
    // Нет ответа в mocks.results -- обещание, которое тест может разрешить
    // сам позже через mocks.resultResolvers[routerID](res); дефолт-резолвер
    // на месте -- цикл не крутится вхолостую до дедлайна по настоящим часам,
    // если тест его не трогает вовсе.
    fetchCommandResult: (routerID) =>
      routerID in mocks.results
        ? Promise.resolve(mocks.results[routerID])
        : new Promise((resolve) => { mocks.resultResolvers[routerID] = resolve }),
    setRouterNotify: (id, muted) => {
      mocks.notify.push({ id, muted })
      if (id in mocks.notifyReplies) return reply(mocks.notifyReplies[id])
      return reply(mocks.notifyReply ?? { muted })
    },
  }
})

const { ParkSection } = await import('../src/screens/ParkSection.jsx')
const { AdminOverlay } = await import('../src/screens/AdminOverlay.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')

// Форма -- miniappFleetResp (miniapp_fleet.go) с полями частей 1-2.
const router = (over) => ({
  id: 0, nickname: '', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.33.0',
  pending_version: '', pending_attempts: 0, pending_last_error_text: '', agent_behind: false,
  agent_update_warning: '', notify_muted: false, ...over,
})
const FLEET = {
  generated_at: '2026-09-15T10:00:00Z',
  totals: { routers: 3, online: 1, sleeping: 1, offline: 1, alerts: 0, pending_deploys: 1 },
  backend: { version: 'v0.33.0', latest_version: '', update_available: false },
  routers: [
    router({ id: 11, nickname: 'bronya', status: 'offline', last_seen_age_sec: 345600, agent_version: 'v0.30.0', pending_version: 'v0.33.0', agent_behind: true }),
    router({ id: 14, nickname: 'office', status: 'sleeping', last_seen_age_sec: 4000, agent_version: 'v0.17.2', agent_behind: true, agent_update_warning: 'проверяет адрес загрузки, должен совпасть с адресом бэкенда' }),
    router({ id: 15, nickname: 'car' }),
  ],
  notify: { unreachable: [], routers_without_recipients: [] },
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

const DOCTOR_OK = ['🩺 Проверка роутера', '✅ awg-manager API: 2.19.1', '✅ tunnels: 2 up'].join('\n')

async function mountPark({ onOpenRouter, currentID } = {}) {
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<ParkSection openSheet={(s) => sheets.push(s)} onOpenRouter={onOpenRouter} currentID={currentID} />, root)
  })
  await flush()
  return { root, sheets }
}

// Лист монтируется отдельно, как в App.jsx: экран отдаёт описание, оболочка
// показывает. Так тест проходит тот же путь, что человек.
async function mountSheet(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  let closed = 0
  await act(async () => {
    render(<Sheet sheet={sheet} asleep={false} onClose={() => closed++} />, root)
  })
  return { root, closed: () => closed }
}

async function typeAndConfirm(sheetRoot, text) {
  const input = sheetRoot.querySelector('#sheet-confirm-input')
  await act(async () => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => [...sheetRoot.querySelectorAll('.sheet-actions button')].pop().click())
  await flush()
  await flush()
}

const buttons = (root, label) => [...root.querySelectorAll('button')].filter((b) => b.textContent === label)
const rowOf = (root, name) => [...root.querySelectorAll('.park-row')].find((r) => r.querySelector('.data-row-main')?.textContent === name)
const cleanup = (...roots) => roots.forEach((r) => { render(null, r); r.remove() })

function reset() {
  mocks.fleet = FLEET
  mocks.fleetCalls = 0
  mocks.updates = []
  mocks.cancels = []
  mocks.fleetUpdates = []
  mocks.sent = []
  mocks.results = {}
  mocks.resultResolvers = {}
  mocks.notify = []
  mocks.notifyReply = null
  mocks.notifyReplies = {}
  mocks.fleetFailAfterFirst = false
}

describe('«Парк»: обновление агента', () => {
  it('выключенный с отложенным обновлением -- «ждёт включения» и «Отменить», без «Обновить»', async () => {
    reset()
    const { root } = await mountPark()
    const row = rowOf(root, 'bronya')
    expect(row.textContent).toContain('ждёт включения: v0.33.0 поставится, когда роутер выйдет на связь')
    expect(buttons(row, 'Отменить обновление')).toHaveLength(1)
    expect(buttons(row, 'Обновить агент')).toHaveLength(0)
    expect(rowOf(root, 'bronya').textContent).toContain('агент v0.30.0 · бэкенд v0.33.0')
    expect(buttons(rowOf(root, 'car'), 'Обновить агент')).toHaveLength(0)
    expect(root.textContent).not.toMatch(/self_update|pending|agent_behind/)
    cleanup(root)
  })

  it('спящий отстающий: набор имени, deferred -- «поставится, когда роутер выйдет на связь», список перечитан', async () => {
    reset()
    mocks.updateReply = { queued: true, deferred: true, target_version: 'v0.33.0' }
    const { root, sheets } = await mountPark()
    const row = rowOf(root, 'office')
    expect(row.textContent).toContain('Оговорка: проверяет адрес загрузки')
    await act(async () => buttons(row, 'Обновить агент')[0].click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].confirmPhrase).toBe('office')
    expect(sheets[0].body).toContain('с v0.17.2 до v0.33.0')
    const sheet = await mountSheet(sheets[0])
    await typeAndConfirm(sheet.root, 'Office')
    expect(mocks.updates).toEqual([{ id: 14, confirm: 'Office', target: undefined }])
    expect(sheet.closed()).toBe(1)
    await flush()
    expect(root.textContent).toContain('Обновление «office» до v0.33.0 поставится, когда роутер выйдет на связь.')
    expect(mocks.fleetCalls).toBe(2)
    cleanup(root, sheet.root)
  })

  it('отказ сервера -- фраза на листе, лист не закрыт, код не показан', async () => {
    reset()
    mocks.updateReply = new ApiError(409, 'deploy_pending', '/routers/14/agent/update failed: 409')
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'office'), 'Обновить агент')[0].click())
    const sheet = await mountSheet(sheets[0])
    await typeAndConfirm(sheet.root, 'office')
    expect(sheet.root.textContent).toContain('Обновление этого роутера уже ждёт своей очереди — сначала отмените его.')
    expect(sheet.root.textContent).not.toContain('deploy_pending')
    expect(sheet.closed()).toBe(0)
    cleanup(root, sheet.root)
  })

  it('отмена: без набора, итог словами, список перечитан', async () => {
    reset()
    mocks.cancelReply = { cleared: true }
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'bronya'), 'Отменить обновление')[0].click())
    expect(sheets[0].confirmPhrase).toBe('')
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(mocks.cancels).toEqual([11])
    expect(root.textContent).toContain('Обновление «bronya» отменено.')
    expect(mocks.fleetCalls).toBe(2)
    cleanup(root, sheet.root)
  })

  it('«Обновить всех отставших (1)»: слово «обновить», итог по роутерам', async () => {
    reset()
    mocks.fleetReply = {
      results: [
        { router_id: 14, nickname: 'office', outcome: 'deferred', reason_code: '', reason_text: '' },
      ],
    }
    const { root, sheets } = await mountPark()
    const all = buttons(root, 'Обновить всех отставших (1)')
    expect(all).toHaveLength(1)
    await act(async () => all[0].click())
    expect(sheets[0].confirmPhrase).toBe('обновить')
    expect(sheets[0].body).toContain('Отстаёт 1 роутер: «office».')
    const sheet = await mountSheet(sheets[0])
    await typeAndConfirm(sheet.root, 'обновить')
    expect(mocks.fleetUpdates).toEqual(['обновить'])
    expect(root.textContent).toContain('Обновление: ждёт включения 1.')
    expect(root.textContent).toContain('«office»: поставится, когда роутер выйдет на связь')
    cleanup(root, sheet.root)
  })

  it('подпись занятости на листе: «Ставим…» для обновления, «Сохраняем…» для уведомлений по умолчанию', async () => {
    reset()
    // Промис перформа не разрешаем сразу -- ловим текст кнопки, пока он «занят».
    let resolveUpdate
    mocks.updateReply = new Promise((r) => { resolveUpdate = r })
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'office'), 'Обновить агент')[0].click())
    const sheet = await mountSheet(sheets[0])
    const input = sheet.root.querySelector('#sheet-confirm-input')
    await act(async () => {
      input.value = 'office'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    expect(buttons(sheet.root, 'Ставим…')).toHaveLength(1)
    resolveUpdate({ queued: true, deferred: false, target_version: 'v0.33.0' })
    await flush()
    await flush()
    cleanup(root, sheet.root)

    reset()
    let resolveNotify
    mocks.notifyReply = new Promise((r) => { resolveNotify = r })
    const mounted = await mountPark()
    await act(async () => rowOf(mounted.root, 'car').querySelector('[role="switch"]').click())
    const muteSheet = await mountSheet(mounted.sheets[0])
    await act(async () => [...muteSheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    expect(buttons(muteSheet.root, 'Сохраняем…')).toHaveLength(1)
    resolveNotify({ muted: true })
    await flush()
    await flush()
    cleanup(mounted.root, muteSheet.root)
  })

  // F2(c): перечитать /fleet после действия не всегда получается (сеть,
  // сервер прилёг), но само действие уже прошло -- список не должен
  // схлопнуться в один общий экран ошибки, только строки до него.
  it('перечитать /fleet после действия не удалось -- строки остаются, ошибка чтения -- отдельной строкой', async () => {
    reset()
    mocks.cancelReply = { cleared: true }
    mocks.fleetFailAfterFirst = true
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'bronya'), 'Отменить обновление')[0].click())
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(root.textContent).toContain('Обновление «bronya» отменено.')
    expect(rowOf(root, 'bronya')).toBeTruthy()
    expect(rowOf(root, 'office')).toBeTruthy()
    expect(rowOf(root, 'car')).toBeTruthy()
    expect(root.textContent).toContain('Не удалось прочитать сводку парка.')
    cleanup(root, sheet.root)
  })

  // F2(d): fleetResult (итог «Обновить всех») и notice (итог одиночного
  // действия) -- разные панели над списком. Старая панель, оставшаяся от
  // прошлого действия, рядом с новой читалась бы как «оба ещё актуальны».
  it('notice и итог «Обновить всех» очищают друг друга при новом действии', async () => {
    reset()
    mocks.cancelReply = { cleared: true }
    mocks.fleetReply = { results: [{ router_id: 14, nickname: 'office', outcome: 'deferred', reason_code: '', reason_text: '' }] }
    const { root, sheets } = await mountPark()

    await act(async () => buttons(root, 'Обновить всех отставших (1)')[0].click())
    const sheet1 = await mountSheet(sheets[0])
    await typeAndConfirm(sheet1.root, 'обновить')
    expect(root.textContent).toContain('Обновление: ждёт включения 1.')
    cleanup(sheet1.root)

    await act(async () => buttons(rowOf(root, 'bronya'), 'Отменить обновление')[0].click())
    const sheet2 = await mountSheet(sheets[1])
    await act(async () => [...sheet2.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(root.textContent).toContain('Обновление «bronya» отменено.')
    expect(root.textContent).not.toContain('Обновление: ждёт включения 1.')
    cleanup(root, sheet2.root)
  })

  it('отставших нет -- кнопки массового обновления нет', async () => {
    reset()
    mocks.fleet = { ...FLEET, routers: [FLEET.routers[0], FLEET.routers[2]] }
    const { root } = await mountPark()
    expect(root.textContent).not.toContain('Обновить всех отставших')
    cleanup(root)
  })
})

describe('устаревший текст про дашборд', () => {
  it('в «Обслуживании и доступах» больше нет «пока живут в браузерном дашборде»', async () => {
    reset()
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => {
      render(<AdminOverlay routerID={11} isAdmin onClose={() => {}} openSheet={() => {}} />, root)
    })
    await flush()
    expect(root.textContent).not.toContain('пока живут в браузерном')
    expect(root.textContent).toContain('bronya')
    cleanup(root)
  })

  it('подпись пункта на экране роутера не отправляет в дашборд', () => {
    // new URL(relative, import.meta.url) здесь резолвится через jsdom (глобальный
    // URL в этом окружении -- не Node'овский): base становится http://localhost,
    // и readFileSync падает на «must be of scheme file». Путь собираем вручную.
    const here = dirname(fileURLToPath(import.meta.url))
    const src = readFileSync(join(here, '../src/screens/RouterDetail.jsx'), 'utf8')
    expect(src).not.toContain('обслуживание пока в дашборде')
  })
})

describe('«Парк»: массовые проверки', () => {
  // FLEET: bronya -- offline, office -- sleeping, car -- online.
  it('«Проверить все»: осмотр уходит только роутеру на связи, пропущенные названы, итог словами', async () => {
    reset()
    mocks.results = { 15: { id: 'c15', status: 'ok', output: '🩺 Проверка роутера\n✅ awg-manager API: 2.19.1\n❌ tunnels: awg12 down' } }
    const { root } = await mountPark()
    await act(async () => buttons(root, 'Проверить все')[0].click())
    await flush()
    await flush()
    expect(mocks.sent).toEqual([{ routerID: 15, action: 'router_doctor', args: {} }])
    expect(root.textContent).toContain('Проверено 1 из 1, проблемы у 1.')
    expect(root.textContent).toContain('«car»: 1 сбой')
    expect(root.textContent).toContain('Не на связи, пропущены: «bronya», «office».')
    expect(root.textContent).not.toMatch(/router_doctor|tunnels/)
    expect(mocks.fleetCalls).toBe(1)
    cleanup(root)
  })

  it('«Аудит всех»: сверка версий, после неё список перечитан', async () => {
    reset()
    mocks.results = {
      15: { id: 'c15', status: 'ok', output: JSON.stringify({ awgmgr_version: '2.19.1', awgmgr_running: true, firmware_current: '4.2.7', firmware_avail: '4.3.0' }) },
    }
    const { root } = await mountPark()
    await act(async () => buttons(root, 'Аудит всех')[0].click())
    await flush()
    await flush()
    expect(mocks.sent).toEqual([{ routerID: 15, action: 'version_audit', args: {} }])
    expect(root.textContent).toContain('Аудит: ответили 1 из 1, внимания требует 1.')
    expect(root.textContent).toContain('«car»: Прошивка роутера — доступна 4.3.0')
    expect(mocks.fleetCalls).toBe(2)
    cleanup(root)
  })

  it('пока идёт одна проверка, обе кнопки погашены', async () => {
    reset()
    mocks.results = {} // ответа нет -- опрос не разрешается
    const { root } = await mountPark()
    await act(async () => buttons(root, 'Проверить все')[0].click())
    expect(buttons(root, 'Проверяем…')[0].disabled).toBe(true)
    expect(buttons(root, 'Аудит всех')[0].disabled).toBe(true)
    expect(root.textContent).toContain('Ответили 0 из 1…')
    cleanup(root)
  })

  // F1(b): двойной тап (два клика раньше, чем Preact перерисует disabled)
  // не должен отправить вторую пачку поверх первой -- защита обязана быть
  // синхронным ref-флагом, а не state, который обновится только на кадре позже.
  it('двойной тап по «Проверить все» шлёт только одну пачку', async () => {
    reset()
    mocks.fleet = { ...FLEET, routers: [FLEET.routers[2]] } // один roundtrip -- car
    const { root } = await mountPark()
    const btn = buttons(root, 'Проверить все')[0]
    await act(async () => {
      btn.click()
      btn.click() // синхронно, до перерисовки -- как настоящий двойной тап
    })
    expect(mocks.sent).toEqual([{ routerID: 15, action: 'router_doctor', args: {} }])
    mocks.resultResolvers[15]({ id: 'c15', status: 'ok', output: DOCTOR_OK })
    await flush()
    await flush()
    expect(root.textContent).toContain('Проверено 1 из 1, проблем не нашлось.')
    cleanup(root)
  })

  // F1(a): уход с экрана прерывает цикл -- уже отправленные запросы
  // доходят, а роутеры в очереди пула (сверх 3 сразу) не опрашиваются, и
  // load() после аудита не зовётся на уже размонтированном экране.
  it('уход с экрана прерывает пачку: очередь не идёт дальше, load() после нет', async () => {
    reset()
    const routers = [15, 16, 17, 18, 19].map((id) => ({ ...FLEET.routers[2], id, nickname: `r${id}` }))
    mocks.fleet = { ...FLEET, routers }
    const { root } = await mountPark()
    await act(async () => buttons(root, 'Аудит всех')[0].click())
    await flush()
    expect(mocks.sent).toHaveLength(3) // пул на 3 -- 4-й и 5-й в очереди
    expect(mocks.fleetCalls).toBe(1) // только монтирование
    cleanup(root) // уход с экрана посреди пачки

    const AUDIT_OK = JSON.stringify({ awgmgr_version: '2.19.1', awgmgr_running: true, firmware_current: '4.3.0', firmware_avail: '4.3.0' })
    for (const id of [15, 16, 17]) mocks.resultResolvers[id]?.({ id: `c${id}`, status: 'ok', output: AUDIT_OK })
    await flush()
    await flush()
    // Роутеры 18 и 19 стояли в очереди пула -- после отмены она не пошла дальше.
    expect(mocks.sent).toHaveLength(3)
    // Аудит перечитывает /fleet по завершении -- но экран уже размонтирован.
    expect(mocks.fleetCalls).toBe(1)
  })
})

describe('«Парк»: уведомлять меня', () => {
  const switchOf = (row) => row.querySelector('[role="switch"]')
  // office: спящий, отстающий, уведомления выключены.
  const MUTED_FLEET = () => ({
    ...FLEET,
    routers: FLEET.routers.map((r) => (r.id === 14 ? { ...r, notify_muted: true } : r)),
  })

  it('выключенный роутер остаётся в списке, с кнопками и примечанием', async () => {
    reset()
    mocks.fleet = MUTED_FLEET()
    const { root } = await mountPark({ onOpenRouter: () => {} })
    const row = rowOf(root, 'office')
    expect(row).toBeTruthy()
    expect(switchOf(row).getAttribute('aria-checked')).toBe('false')
    expect(row.textContent).toContain('Бот не пишет вам про этот роутер. Его экраны открываются как обычно.')
    expect(buttons(row, 'Обновить агент')[0].disabled).toBe(false)
    expect(buttons(row, 'Открыть роутер')[0].disabled).toBe(false)
    expect(root.textContent).toContain('Обновить всех отставших (1)')
    cleanup(root)
  })

  it('выключение -- через лист, после него роутер на месте и переключатель выключен', async () => {
    reset()
    mocks.notifyReply = { muted: true }
    const { root, sheets } = await mountPark()
    const row = rowOf(root, 'car')
    expect(switchOf(row).getAttribute('aria-checked')).toBe('true')
    await act(async () => switchOf(row).click())
    expect(mocks.notify).toEqual([])
    expect(sheets[0].title).toBe('Не уведомлять вас про «car»?')
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(mocks.notify).toEqual([{ id: 15, muted: true }])
    const after = rowOf(root, 'car')
    expect(after).toBeTruthy()
    expect(switchOf(after).getAttribute('aria-checked')).toBe('false')
    expect(root.querySelectorAll('.park-row')).toHaveLength(3)
    cleanup(root, sheet.root)
  })

  it('включение обратно -- сразу, без листа', async () => {
    reset()
    mocks.fleet = MUTED_FLEET()
    mocks.notifyReply = { muted: false }
    const { root, sheets } = await mountPark()
    await act(async () => switchOf(rowOf(root, 'office')).click())
    await flush()
    expect(sheets).toEqual([])
    expect(mocks.notify).toEqual([{ id: 14, muted: false }])
    expect(switchOf(rowOf(root, 'office')).getAttribute('aria-checked')).toBe('true')
    cleanup(root)
  })

  it('сбой сохранения -- фраза у строки, переключатель не соврал', async () => {
    reset()
    mocks.fleet = MUTED_FLEET()
    mocks.notifyReply = new ApiError(500, 'internal', '/routers/14/notify failed: 500')
    const { root } = await mountPark()
    await act(async () => switchOf(rowOf(root, 'office')).click())
    await flush()
    const row = rowOf(root, 'office')
    expect(row.textContent).toContain('Не удалось сохранить. Попробуйте ещё раз.')
    expect(switchOf(row).getAttribute('aria-checked')).toBe('false')
    cleanup(root)
  })

  // F2(e): выключение (не включение обратно) идёт через лист подтверждения --
  // отказ там срабатывает другим путём (Sheet.jsx local perform), и раньше
  // этот путь не был проверен вовсе.
  it('выключение, упавшее внутри листа подтверждения, -- ошибка у строки, переключатель остаётся включён', async () => {
    reset()
    mocks.notifyReply = new ApiError(500, 'internal', '/routers/15/notify failed: 500')
    const { root, sheets } = await mountPark()
    const row = rowOf(root, 'car')
    expect(switchOf(row).getAttribute('aria-checked')).toBe('true')
    await act(async () => switchOf(row).click())
    expect(sheets).toHaveLength(1)
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    const after = rowOf(root, 'car')
    expect(switchOf(after).getAttribute('aria-checked')).toBe('true')
    expect(after.textContent).toContain('Не удалось сохранить. Попробуйте ещё раз.')
    cleanup(root, sheet.root)
  })

  // F2(a): notifyBusy/notifyError живут по роутеру (map), а не одним общим
  // значением -- иначе переключение второй строки гасило бы занятость и
  // ошибку первой, хотя её запрос к серверу ещё не завершился.
  it('переключение двух роутеров не мешает друг другу: занятость и ошибка -- по роутеру', async () => {
    reset()
    mocks.fleet = { ...FLEET, routers: [FLEET.routers[2], { ...FLEET.routers[2], id: 20, nickname: 'lux' }] }
    let resolveCar
    let rejectLux
    mocks.notifyReplies = {
      15: new Promise((r) => { resolveCar = r }),
      20: new Promise((_, rej) => { rejectLux = rej }),
    }
    const { root, sheets } = await mountPark()
    await act(async () => switchOf(rowOf(root, 'car')).click())
    await act(async () => switchOf(rowOf(root, 'lux')).click())
    expect(sheets).toHaveLength(2)
    const sheetCar = await mountSheet(sheets[0])
    const sheetLux = await mountSheet(sheets[1])
    await act(async () => [...sheetCar.root.querySelectorAll('.sheet-actions button')].pop().click())
    await act(async () => [...sheetLux.root.querySelectorAll('.sheet-actions button')].pop().click())
    expect(switchOf(rowOf(root, 'car')).disabled).toBe(true)
    expect(switchOf(rowOf(root, 'lux')).disabled).toBe(true)

    rejectLux(new ApiError(500, 'internal', '/routers/20/notify failed: 500'))
    await flush()
    await flush()
    // lux settled with an error; car's own busy/error must be untouched by it.
    expect(rowOf(root, 'lux').textContent).toContain('Не удалось сохранить. Попробуйте ещё раз.')
    expect(switchOf(rowOf(root, 'lux')).getAttribute('aria-checked')).toBe('true')
    expect(switchOf(rowOf(root, 'lux')).disabled).toBe(false)
    expect(switchOf(rowOf(root, 'car')).disabled).toBe(true)
    expect(rowOf(root, 'car').textContent).not.toContain('Не удалось сохранить')

    resolveCar({ muted: true })
    await flush()
    await flush()
    expect(switchOf(rowOf(root, 'car')).getAttribute('aria-checked')).toBe('false')
    expect(switchOf(rowOf(root, 'car')).disabled).toBe(false)
    cleanup(root, sheetCar.root, sheetLux.root)
  })

  it('«Открыть роутер» ведёт на экран роутера и у выключенного; у текущего кнопки нет', async () => {
    reset()
    mocks.fleet = MUTED_FLEET()
    const opened = []
    const { root } = await mountPark({ onOpenRouter: (id) => opened.push(id), currentID: 15 })
    await act(async () => buttons(rowOf(root, 'office'), 'Открыть роутер')[0].click())
    expect(opened).toEqual([14])
    expect(buttons(rowOf(root, 'car'), 'Открыть роутер')).toHaveLength(0)
    cleanup(root)
  })

  it('AdminOverlay пробрасывает переход на роутер', async () => {
    reset()
    const opened = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => {
      render(<AdminOverlay routerID={15} isAdmin onClose={() => {}} openSheet={() => {}} onOpenRouter={(id) => opened.push(id)} />, root)
    })
    await flush()
    await act(async () => buttons(rowOf(root, 'bronya'), 'Открыть роутер')[0].click())
    expect(opened).toEqual([11])
    cleanup(root)
  })
})
