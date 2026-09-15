import { describe, it, expect } from 'vitest'
import {
  BATCH,
  batchTargets,
  doctorProblems,
  auditProblems,
  runFleetBatch,
  batchProgressLine,
  batchSummary,
} from '../src/fleetBatch.js'

// Роутеры -- в форме miniappFleetRouter (miniapp_fleet.go).
const R = (id, nickname, status) => ({ id, nickname, status, last_seen_age_sec: 30, agent_version: 'v0.33.0', pending_version: '', pending_attempts: 0, pending_last_error_text: '', agent_behind: false, agent_update_warning: '', notify_muted: false })

// Настоящий формат ответа агента: internal/agent/actions/router_doctor.go
// (заголовок «🩺», строки «✅|⚠️|❌ имя: подробность»).
const DOCTOR_OK = ['🩺 Проверка роутера', '✅ awg-manager API: 2.19.1', '✅ tunnels: 2 up', '✅ pingcheck: 2 ok'].join('\n')
const DOCTOR_BAD = ['🩺 Проверка роутера', '✅ awg-manager API: 2.19.1', '⚠️ pingcheck: disabled', '⚠️ memory: router is marked low-memory by awg-manager', '❌ tunnels: awg12 down'].join('\n')
// Настоящий формат version_audit: wire.VersionAudit (pkg/wire/maintenance.go).
const AUDIT_OK = JSON.stringify({ awgmgr_version: '2.19.1', awgmgr_running: true, hrneo_installed: true, hrneo_running: true, hrneo_version: '3.18.3', firmware_current: '4.3.0', firmware_avail: '4.3.0' })
const AUDIT_BAD = JSON.stringify({ awgmgr_version: '2.19.1', awgmgr_running: true, hrneo_installed: true, hrneo_running: false, hrneo_version: '3.18.3', firmware_current: '4.2.7', firmware_avail: '4.3.0' })

describe('кого опрашивать', () => {
  it('выключенные и спящие пропускаются, тревога и живые -- опрашиваются', () => {
    const { targets, skipped } = batchTargets([R(1, 'home', 'online'), R(2, 'car', 'sleeping'), R(3, 'bronya', 'offline'), R(4, 'dacha', 'alert')])
    expect(targets.map((r) => r.id)).toEqual([1, 4])
    expect(skipped.map((r) => r.id)).toEqual([2, 3])
    expect(batchTargets(undefined)).toEqual({ targets: [], skipped: [] })
  })

  // Пре-флайт 15.09: gachimikhail -- статус alert, но молчит 12 суток. Ждать его 90 с нельзя.
  it('тревога у давно молчащего роутера -- пропуск, как выключенный', () => {
    const silent = { ...R(9, 'gachimikhail', 'alert'), last_seen_age_sec: 1040226 }
    const { targets, skipped } = batchTargets([R(1, 'home', 'online'), silent])
    expect(targets.map((r) => r.id)).toEqual([1])
    expect(skipped.map((r) => r.id)).toEqual([9])
  })

  it('действия -- осмотр и аудит из белого списка', () => {
    expect(BATCH.doctor.action).toBe('router_doctor')
    expect(BATCH.audit.action).toBe('version_audit')
    expect(BATCH.doctor.idle).toBe('Проверить все')
    expect(BATCH.audit.idle).toBe('Аудит всех')
  })
})

describe('разбор ответов', () => {
  it('осмотр: считает сбои и замечания, английские подробности наружу не идут', () => {
    expect(doctorProblems(DOCTOR_BAD)).toEqual({ fails: 1, warns: 2 })
    expect(doctorProblems(DOCTOR_OK)).toEqual({ fails: 0, warns: 0 })
  })

  it('осмотр: ответ без строк -- не разобран, а не «всё хорошо»', () => {
    expect(doctorProblems('песочница: router_doctor выполнен')).toBeNull()
    expect(doctorProblems('')).toBeNull()
  })

  it('аудит: проблемы -- русскими строками экрана настроек', () => {
    expect(auditProblems(AUDIT_BAD)).toEqual(['Обход блокировок — установлен, но не работает', 'Прошивка роутера — доступна 4.3.0'])
    expect(auditProblems(AUDIT_OK)).toEqual([])
    expect(auditProblems('не json')).toBeNull()
  })
})

// Часы и сеть -- внедрённые: цикл проверяется без таймеров.
function fakeNet(replies) {
  const sent = []
  let t = 0
  return {
    sent,
    now: () => t,
    send: (id, action, args) => {
      sent.push({ id, action, args })
      const r = replies[id]
      return r === 'throw' ? Promise.reject(new Error('net')) : Promise.resolve({ cmd_id: `c${id}` })
    },
    poll: (id) => {
      t += 30_000
      const r = replies[id]
      return Promise.resolve(r === 'silent' || r === 'throw' ? null : r)
    },
  }
}

describe('цикл массовой команды', () => {
  it('шлёт действие каждому на связи, пропущенных называет, итог по исходам', async () => {
    const net = fakeNet({
      1: { id: 'c1', status: 'ok', output: DOCTOR_OK },
      4: { id: 'c4', status: 'ok', output: DOCTOR_BAD },
      5: 'silent',
      6: { id: 'c6', status: 'err', output: 'boom' },
      7: { id: 'c7', status: 'ok', output: 'непонятно' },
      8: 'throw',
    })
    const progress = []
    const state = await runFleetBatch({
      kind: 'doctor',
      routers: [R(1, 'home', 'online'), R(2, 'car', 'sleeping'), R(3, 'bronya', 'offline'), R(4, 'dacha', 'alert'), R(5, 'garage', 'online'), R(6, 'shed', 'online'), R(7, 'barn', 'online'), R(8, 'lake', 'online')],
      send: net.send,
      poll: net.poll,
      now: net.now,
      deadlineMs: 90_000,
      onProgress: (s) => progress.push(s),
    })
    expect(net.sent.map((s) => s.id).sort()).toEqual([1, 4, 5, 6, 7, 8])
    expect(net.sent.every((s) => s.action === 'router_doctor')).toBe(true)
    expect(state.total).toBe(6)
    expect(state.done).toBe(6)
    expect(state.running).toBe(false)
    expect(state.skipped).toEqual(['car', 'bronya'])
    const by = Object.fromEntries(state.results.map((r) => [r.nickname, r.outcome]))
    expect(by).toEqual({ home: 'ok', dacha: 'problems', garage: 'no_answer', shed: 'failed', barn: 'unparsed', lake: 'no_answer' })
    // Каждая запись несёт router_id -- итог кладёт по нему ключ строки, а не по тексту.
    const ids = Object.fromEntries(state.results.map((r) => [r.nickname, r.id]))
    expect(ids).toEqual({ home: 1, dacha: 4, garage: 5, shed: 6, barn: 7, lake: 8 })
    expect(progress[0]).toMatchObject({ total: 6, done: 0, running: true })
    expect(progress.at(-1)).toMatchObject({ done: 6, running: false })
  })

  it('все не на связи -- ни одной команды', async () => {
    const net = fakeNet({})
    const state = await runFleetBatch({ kind: 'audit', routers: [R(2, 'car', 'sleeping')], send: net.send, poll: net.poll, now: net.now })
    expect(net.sent).toEqual([])
    expect(state).toMatchObject({ total: 0, done: 0, running: false, skipped: ['car'] })
  })
})

describe('итог словами', () => {
  const base = { kind: 'doctor', total: 6, done: 6, running: false, skipped: ['car', 'bronya'] }

  it('ход -- «ответили N из M…»', () => {
    expect(batchProgressLine({ ...base, done: 2, running: true })).toBe('Ответили 2 из 6…')
    expect(batchProgressLine({ ...base, done: 1, running: true })).toBe('Ответил 1 из 6…')
    expect(batchProgressLine(null)).toBe('')
  })

  it('осмотр: проверено N, проблемы у M, по роутерам -- числа, отсортировано по имени', () => {
    const s = batchSummary({
      ...base,
      results: [
        { id: 1, nickname: 'home', outcome: 'ok' },
        { id: 4, nickname: 'dacha', outcome: 'problems', problems: { fails: 1, warns: 2 } },
        { id: 5, nickname: 'garage', outcome: 'no_answer' },
        { id: 6, nickname: 'shed', outcome: 'failed' },
        { id: 7, nickname: 'barn', outcome: 'unparsed' },
        { id: 8, nickname: 'lake', outcome: 'no_answer' },
      ],
    })
    expect(s.headline).toBe('Проверено 3 из 6, проблемы у 1.')
    // Ключ строки -- router_id, не текст: две строки с одинаковым текстом
    // (два «не ответил») не должны схлопнуться в один ключ React/Preact.
    expect(s.lines).toEqual([
      { id: 7, text: '«barn»: ответ не разобран' },
      { id: 4, text: '«dacha»: 1 сбой, 2 замечания' },
      { id: 5, text: '«garage»: не ответил' },
      { id: 8, text: '«lake»: не ответил' },
      { id: 6, text: '«shed»: команда не выполнилась' },
      { id: 'skipped', text: 'Не на связи, пропущены: «car», «bronya».' },
      { id: 'doctor-note', text: 'Что именно не так — на экране роутера: «Настройки» → «Осмотр роутера».' },
    ])
    expect(new Set(s.lines.map((l) => l.id)).size).toBe(s.lines.length)
  })

  it('осмотр без проблем -- «проблем не нашлось», подсказки про экран нет', () => {
    const s = batchSummary({ kind: 'doctor', total: 1, done: 1, running: false, skipped: [], results: [{ id: 1, nickname: 'home', outcome: 'ok' }] })
    expect(s).toEqual({ headline: 'Проверено 1 из 1, проблем не нашлось.', lines: [] })
  })

  it('аудит: внимания требуют M, строки экрана настроек', () => {
    const s = batchSummary({
      kind: 'audit', total: 2, done: 2, running: false, skipped: ['car'],
      results: [
        { id: 1, nickname: 'home', outcome: 'ok' },
        { id: 4, nickname: 'dacha', outcome: 'problems', problems: ['Прошивка роутера — доступна 4.3.0'] },
      ],
    })
    expect(s.headline).toBe('Аудит: ответили 2 из 2, внимания требует 1.')
    expect(s.lines).toEqual([
      { id: 4, text: '«dacha»: Прошивка роутера — доступна 4.3.0' },
      { id: 'skipped', text: 'Не на связи, пропущен: «car».' },
    ])
  })

  it('все не на связи -- говорит это, а не «проверено 0 из 0»', () => {
    expect(batchSummary({ kind: 'audit', total: 0, done: 0, running: false, skipped: ['car'], results: [] }).headline).toBe(
      'Все роутеры не на связи — сверять некого.',
    )
    expect(batchSummary({ kind: 'doctor', total: 0, done: 0, running: false, skipped: ['car'], results: [] }).headline).toBe(
      'Все роутеры не на связи — проверять некого.',
    )
  })

  it('в итоге нет внутренних имён', () => {
    const s = batchSummary({ ...base, results: [{ id: 4, nickname: 'dacha', outcome: 'problems', problems: { fails: 5, warns: 11 } }] })
    expect([s.headline, ...s.lines.map((l) => l.text)].join(' ')).not.toMatch(/router_doctor|version_audit|pingcheck|tunnels/)
    expect(s.lines[0]).toEqual({ id: 4, text: '«dacha»: 5 сбоев, 11 замечаний' })
  })
})
