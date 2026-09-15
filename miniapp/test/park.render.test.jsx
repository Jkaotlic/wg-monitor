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
  linkDeferred: false, linkResolver: null,
  revives: [], reviveReply: null, reviveCancels: [], reviveCancelReply: null,
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
    // mocks.linkDeferred -- тест сам решает, когда ссылка «приедет», чтобы
    // поймать момент между уходом с экрана и разрешением промиса.
    createWebLink: () =>
      mocks.linkDeferred
        ? new Promise((resolve) => { mocks.linkResolver = resolve })
        : Promise.resolve({ url: 'https://wg.example.com/x', notice: '', limit_notice: '' }),
    updateRouterAgent: (id, confirm, target) => {
      mocks.updates.push({ id, confirm, target })
      return reply(mocks.updateReply)
    },
    cancelRouterAgentUpdate: (id) => {
      mocks.cancels.push(id)
      return reply(mocks.cancelReply)
    },
    reviveRouterAgent: (id, body) => {
      mocks.revives.push({ id, body })
      return reply(mocks.reviveReply)
    },
    cancelRouterAgentRevive: (id) => {
      mocks.reviveCancels.push(id)
      return reply(mocks.reviveCancelReply)
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
  mocks.linkDeferred = false
  mocks.linkResolver = null
  mocks.revives = []
  mocks.reviveReply = null
  mocks.reviveCancels = []
  mocks.reviveCancelReply = null
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

  // Cross-batch (B6, backend review): агент ниже agentSelfUpdateFloor не
  // умеет self_update вовсе -- сервер шлёт agent_behind=false и
  // предупреждение «нужна переустановка». Раньше warning показывался только
  // рядом с кнопкой «Обновить» (canUpdate), а у такого роутера её нет --
  // предупреждение было невидимо.
  it('слишком старый агент: предупреждение видно без кнопки «Обновить» и не в счётчике отставших', async () => {
    reset()
    mocks.fleet = {
      ...FLEET,
      routers: [
        ...FLEET.routers,
        router({ id: 21, nickname: 'antique', status: 'online', agent_version: 'v0.10.0', agent_behind: false, agent_update_warning: 'агент слишком старый — нужна переустановка' }),
      ],
    }
    const { root } = await mountPark()
    const row = rowOf(root, 'antique')
    expect(row.textContent).toContain('Оговорка: агент слишком старый — нужна переустановка')
    expect(buttons(row, 'Обновить агент')).toHaveLength(0)
    // office (agent_behind:true) -- единственный в счётчике; antique его не увеличивает.
    expect(root.textContent).toContain('Обновить всех отставших (1)')
    cleanup(root)
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
    expect(root.textContent).toContain('Готово 0 из 1…')
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

// Fix round 1, Minor #4 (review-minors-miniapp.md): saveNotify и
// openInBrowser не проверяли aliveRef -- в Preact это не падает, но
// открывать ссылку в браузере (побочный эффект, не только setState) на
// экране, который уже покинули, не должно происходить.
describe('«Парк»: уход с экрана гасит отложенные действия', () => {
  it('уход с экрана до ответа createWebLink -- ссылка не открывается, когда ответ пришёл позже', async () => {
    reset()
    mocks.linkDeferred = true
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    const { root } = await mountPark()
    await act(async () => buttons(root, 'Открыть в браузере')[0].click())
    cleanup(root) // уход с экрана, пока createWebLink ещё в пути
    mocks.linkResolver({ url: 'https://wg.example.com/x', notice: '', limit_notice: '' })
    await flush()
    await flush()
    expect(openSpy).not.toHaveBeenCalled()
    openSpy.mockRestore()
  })
})

describe('«Парк»: оживление агента', () => {
  const SECRET = 'root-Пароль-9f3kq'
  const rv = (over) => ({
    status: 'waiting', expires_at: '2026-10-15T12:00:00Z', attempts: 0,
    last_error_text: '', last_probe_text: '', last_probe_at: '', ...over,
  })
  // Парк 15.09: caredns-oldcar (адрес панели есть), bronya (адреса нет),
  // gachimikhail (тревога при молчании -- away с сервера).
  const FLEET_REVIVE = {
    ...FLEET,
    generated_at: '2026-09-15T10:00:00Z',
    revive_enabled: true,
    routers: [
      router({ id: 21, nickname: 'caredns-oldcar', status: 'offline', away: true, last_seen_age_sec: 900000, agent_version: 'v0.24.1', panel_address_known: true,
        revive: rv({ last_probe_text: 'не отвечает', last_probe_at: '2026-09-15T09:58:00Z' }) }),
      router({ id: 22, nickname: 'bronya', status: 'offline', away: true, last_seen_age_sec: 900000, agent_version: '', panel_address_known: false, revive: null }),
      router({ id: 23, nickname: 'gachimikhail', status: 'alert', away: true, last_seen_age_sec: 1036800, panel_address_known: true,
        revive: rv({ status: 'failed', last_error_text: 'пароль не подошёл' }) }),
      router({ id: 15, nickname: 'car', panel_address_known: true, revive: null }),
    ],
  }
  const INTERNAL = /awgm|root_password|api_key|revive|waiting|running|probe|intent/

  async function fillField(sheetRoot, name, value) {
    const el = sheetRoot.querySelector(`#sheet-field-${name}`)
    await act(async () => {
      el.value = value
      el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
    })
  }

  it('строки: ждёт с отменой, «не вышло» с повтором, у роутера без намерения -- «Оживить», у живого -- ничего', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    const { root } = await mountPark()
    const oldcar = rowOf(root, 'caredns-oldcar')
    expect(oldcar.textContent).toContain('оживление: ждёт роутер · проверка 2 мин назад: не отвечает')
    expect(buttons(oldcar, 'Отменить оживление')).toHaveLength(1)
    expect(buttons(oldcar, 'Оживить агент')).toHaveLength(0)
    const bronya = rowOf(root, 'bronya')
    expect(buttons(bronya, 'Оживить агент')).toHaveLength(1)
    expect(bronya.textContent).not.toContain('оживление:')
    const gachi = rowOf(root, 'gachimikhail')
    expect(gachi.textContent).toContain('оживление: не вышло: пароль не подошёл')
    expect(gachi.querySelector('.park-update-danger')).not.toBeNull()
    expect(buttons(gachi, 'Оживить агент')).toHaveLength(1)
    const car = rowOf(root, 'car')
    expect(buttons(car, 'Оживить агент')).toHaveLength(0)
    expect(car.textContent).not.toContain('оживление')
    expect(root.textContent).not.toContain('не настроено на сервере')
    expect(root.textContent).not.toMatch(INTERNAL)
    cleanup(root)
  })

  it('оживляется, ожил, срок истёк -- словами', async () => {
    reset()
    mocks.fleet = {
      ...FLEET_REVIVE,
      routers: [
        router({ id: 31, nickname: 'r-running', status: 'offline', away: true, revive: rv({ status: 'running' }) }),
        router({ id: 32, nickname: 'r-done', status: 'online', away: false, revive: rv({ status: 'done' }) }),
        router({ id: 33, nickname: 'r-expired', status: 'offline', away: true, revive: rv({ status: 'expired' }) }),
      ],
    }
    const { root } = await mountPark()
    expect(rowOf(root, 'r-running').textContent).toContain('оживление: оживляется…')
    expect(rowOf(root, 'r-running').querySelectorAll('.park-actions button')).toHaveLength(0)
    // Идёт прямо сейчас -- сигнальный цвет, не приглушённый «ничего не происходит».
    expect(rowOf(root, 'r-running').querySelector('.park-update-sig')).not.toBeNull()
    expect(rowOf(root, 'r-done').textContent).toContain('оживление: ожил')
    expect(rowOf(root, 'r-done').querySelector('.park-update-ok')).not.toBeNull()
    expect(rowOf(root, 'r-expired').textContent).toContain('оживление: срок истёк')
    expect(buttons(rowOf(root, 'r-expired'), 'Оживить агент')).toHaveLength(1)
    cleanup(root)
  })

  it('bronya: лист с паролем и адресом панели, тело запроса, итог словами, пароль нигде не остался', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    mocks.reviveReply = { status: 'waiting', expires_at: '2026-10-15T12:00:00Z' }
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'bronya'), 'Оживить агент')[0].click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].title).toBe('Оживить агент на «bronya»?')
    expect(sheets[0].confirmPhrase).toBe('bronya')
    expect(sheets[0].note).toBe('Пароль хранится на сервере зашифрованным до оживления, потом стирается.')
    const sheet = await mountSheet(sheets[0])
    expect(sheet.root.querySelector('#sheet-field-awgm_url')).not.toBeNull()
    expect(sheet.root.querySelector('#sheet-field-root_password').type).toBe('password')
    expect(sheet.root.querySelector('#sheet-field-expires_days').value).toBe('30')
    await fillField(sheet.root, 'root_password', SECRET)
    await fillField(sheet.root, 'awgm_url', ' https://192.168.1.1 ')
    await typeAndConfirm(sheet.root, 'Bronya')
    expect(mocks.revives).toEqual([
      { id: 22, body: { confirm: 'Bronya', expires_days: 30, root_password: SECRET, awgm_url: 'https://192.168.1.1' } },
    ])
    expect(sheet.closed()).toBe(1)
    await flush()
    expect(root.textContent).toContain('Оживление «bronya» поставлено: агент переустановится, когда роутер выйдет на связь. Ждём до 15.10.2026.')
    expect(mocks.fleetCalls).toBe(2)
    expect(JSON.stringify(sheets[0])).not.toContain(SECRET)
    expect(root.innerHTML).not.toContain(SECRET)
    expect(sheet.root.innerHTML).not.toContain(SECRET)
    cleanup(root, sheet.root)
  })

  it('роутер с известным адресом панели -- поля адреса нет; без пароля root кнопка не горит даже со входом в панель', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'gachimikhail'), 'Оживить агент')[0].click())
    const sheet = await mountSheet(sheets[0])
    expect(sheet.root.querySelector('#sheet-field-awgm_url')).toBeNull()
    const input = sheet.root.querySelector('#sheet-confirm-input')
    await act(async () => {
      input.value = 'gachimikhail'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const primary = () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop()
    expect(primary().disabled).toBe(true)
    await fillField(sheet.root, 'awgm_login', 'admin')
    await fillField(sheet.root, 'awgm_password', 'p')
    await fillField(sheet.root, 'awgm_api_key', 'k')
    expect(primary().disabled).toBe(true)
    await fillField(sheet.root, 'root_password', 'r')
    expect(primary().disabled).toBe(false)
    expect([...sheet.root.querySelectorAll('#sheet-field-expires_days option')].map((o) => o.value)).toEqual(['7', '14', '30'])
    cleanup(root, sheet.root)
  })

  it('отказ сервера -- фраза на листе, лист не закрыт, код не показан, пароль стёрт', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    mocks.reviveReply = new ApiError(409, 'agent_alive', '/routers/23/agent/revive failed: 409', 'Агент на роутере отвечает — оживлять нечего.')
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'gachimikhail'), 'Оживить агент')[0].click())
    const sheet = await mountSheet(sheets[0])
    // Пре-флайт 15.09: пароль root обязателен, вход в панель -- в дополнение.
    await fillField(sheet.root, 'root_password', SECRET)
    await fillField(sheet.root, 'awgm_login', 'admin')
    await fillField(sheet.root, 'awgm_password', SECRET)
    await fillField(sheet.root, 'expires_days', '14')
    await typeAndConfirm(sheet.root, 'gachimikhail')
    expect(mocks.revives[0].body).toEqual({ confirm: 'gachimikhail', expires_days: 14, root_password: SECRET, awgm_login: 'admin', awgm_password: SECRET })
    expect(sheet.root.textContent).toContain('Агент на роутере отвечает — оживлять нечего.')
    expect(sheet.root.textContent).not.toContain('agent_alive')
    expect(sheet.closed()).toBe(0)
    expect(sheet.root.querySelector('#sheet-field-awgm_password').value).toBe('')
    expect(sheet.root.querySelector('#sheet-field-root_password').value).toBe('')
    expect(sheet.root.innerHTML).not.toContain(SECRET)
    expect(JSON.stringify(sheets[0])).not.toContain(SECRET)
    cleanup(root, sheet.root)
  })

  it('отмена оживления: без набора, итог словами, список перечитан', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    mocks.reviveCancelReply = { cleared: true }
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'caredns-oldcar'), 'Отменить оживление')[0].click())
    expect(sheets[0].title).toBe('Отменить оживление агента на «caredns-oldcar»?')
    expect(sheets[0].confirmPhrase).toBe('')
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(mocks.reviveCancels).toEqual([21])
    expect(root.textContent).toContain('Оживление «caredns-oldcar» отменено, пароль стёрт.')
    expect(mocks.fleetCalls).toBe(2)
    cleanup(root, sheet.root)
  })

  it('отмена во время переустановки -- фраза экрана, лист не закрыт', async () => {
    reset()
    mocks.fleet = FLEET_REVIVE
    mocks.reviveCancelReply = new ApiError(409, 'revive_running', '/routers/21/agent/revive failed: 409', 'Оживление уже идёт — дождитесь итога.')
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'caredns-oldcar'), 'Отменить оживление')[0].click())
    const sheet = await mountSheet(sheets[0])
    await act(async () => [...sheet.root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    await flush()
    expect(sheet.root.textContent).toContain('Оживление уже идёт — дождитесь итога.')
    expect(sheet.root.textContent).not.toContain('revive_running')
    expect(sheet.closed()).toBe(0)
    cleanup(root, sheet.root)
  })

  it('оживление не настроено на сервере: одна строка, ни одной кнопки оживления, ход виден', async () => {
    reset()
    mocks.fleet = { ...FLEET_REVIVE, revive_enabled: false }
    const { root } = await mountPark()
    expect(root.textContent.split('Оживление агента не настроено на сервере.')).toHaveLength(2)
    expect(buttons(root, 'Оживить агент')).toHaveLength(0)
    expect(buttons(root, 'Отменить оживление')).toHaveLength(0)
    expect(rowOf(root, 'caredns-oldcar').textContent).toContain('оживление: ждёт роутер')
    cleanup(root)
  })
})
