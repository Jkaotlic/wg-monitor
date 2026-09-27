import { describe, it, expect } from 'vitest'
import { repairView } from '../src/repair.js'

// MINI-05. Форма ответа GET /repair -- miniappReplaceResp: job_id, state,
// hint, steps[{name,status,detail}], running. Имени туннеля в нём нет:
// сервер отдаёт последнюю починку РОУТЕРА (LatestFor по нику).
const RUNNING = {
  job_id: 'j-2',
  state: 'running',
  running: true,
  steps: [
    { name: 'failover', status: 'done' },
    { name: 'issue', status: 'active' },
    { name: 'failback', status: 'pending' },
  ],
}

describe('MINI-05: экран починки не врёт о ходе', () => {
  it('ответ ещё не пришёл -- не «Чиню»', () => {
    const v = repairView(null, { checkName: 'tunnel_awg10' })
    expect(v.title).not.toBe('Чиню')
    expect(v.loading).toBe(true)
  })
  it('пустой ответ (починки не было) -- не «Чиню»', () => {
    const v = repairView({ running: false }, { checkName: 'tunnel_awg10' })
    expect(v.title).not.toBe('Чиню')
    expect(v.idle).toBe(true)
  })
  it('чужое задание без имени туннеля названо починкой роутера, а не этого туннеля', () => {
    const v = repairView(RUNNING, { checkName: 'tunnel_awg10' })
    expect(v.scope).toBe('router')
    expect(v.title).toContain('роутер')
  })
  it('своё задание (запущено с этого экрана) -- про этот туннель', () => {
    const v = repairView(RUNNING, { checkName: 'tunnel_awg10', ownJobID: 'j-2' })
    expect(v.scope).toBe('this')
  })
})

// Бэкенд v0.46 кладёт в ответ /repair check_name и tunnel_id (omitempty).
describe('MINI-05: сервер называет туннель починки', () => {
  it('тот же туннель -- ход этого туннеля, даже без своего job_id', () => {
    const v = repairView({ ...RUNNING, check_name: 'tunnel_awg10', tunnel_id: 'awg10' }, { checkName: 'tunnel_awg10' })
    expect(v.scope).toBe('this')
    expect(v.title).toBe('Поднимаю связь')
  })
  it('другой туннель чинится сейчас -- так и сказано', () => {
    const v = repairView({ ...RUNNING, check_name: 'tunnel_awg11', tunnel_id: 'awg11' }, { checkName: 'tunnel_awg10' })
    expect(v.scope).toBe('other')
    expect(v.title).toContain('другой')
  })
  it('другой туннель, починка закончена -- для этого туннеля починки не было', () => {
    const v = repairView(
      { job_id: 'j-3', state: 'failed', running: false, check_name: 'tunnel_awg11', tunnel_id: 'awg11', steps: [{ name: 'issue', status: 'failed', detail: 'кабинет не ответил' }] },
      { checkName: 'tunnel_awg10' },
    )
    expect(v.scope).toBe('other')
    expect(v.done).toBe(false)
    expect(v.idle).toBe(true)
    expect(v.note).toBe('')
    expect(v.steps.every((s) => s.state === 'pending')).toBe(true)
  })
})
