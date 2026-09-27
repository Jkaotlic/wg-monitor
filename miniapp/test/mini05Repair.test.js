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
