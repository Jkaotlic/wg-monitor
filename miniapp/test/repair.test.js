import { describe, it, expect } from 'vitest'
import { repairView } from '../src/repair.js'

describe('repairView', () => {
  // Восемь машинных шагов задания человеку не нужны: он хочет знать, на
  // какой из трёх понятных стадий сейчас находится починка.
  it('сворачивает чеклист задания в три шага', () => {
    const v = repairView({
      state: 'running',
      steps: [
        { name: 'failover', status: 'done' },
        { name: 'issue', status: 'active' },
        { name: 'import', status: 'pending' },
        { name: 'handshake', status: 'pending' },
        { name: 'promote', status: 'pending' },
        { name: 'verify', status: 'pending' },
        { name: 'retire', status: 'pending' },
        { name: 'failback', status: 'pending' },
      ],
    })
    expect(v.steps.map((s) => s.key)).toEqual(['failover', 'reissue', 'failback'])
    expect(v.steps[0].state).toBe('done')
    expect(v.steps[1].state).toBe('active')
    expect(v.steps[2].state).toBe('pending')
    expect(v.done).toBe(false)
  })

  // Провал обязан читаться как провал, а не как вечное «ждёт».
  it('не обещает продолжения у законченного задания', () => {
    const v = repairView({
      state: 'failed',
      steps: [
        { name: 'failover', status: 'done' },
        { name: 'issue', status: 'failed', detail: 'кабинет не ответил' },
        { name: 'failback', status: 'pending' },
      ],
    })
    expect(v.done).toBe(true)
    expect(v.ok).toBe(false)
    expect(v.steps[2].state).toBe('skipped')
    expect(v.note).toContain('кабинет не ответил')
  })

  it('успешное задание закрывает все три шага', () => {
    const v = repairView({
      state: 'success',
      steps: [
        { name: 'failover', status: 'done' },
        { name: 'issue', status: 'done' },
        { name: 'import', status: 'done' },
        { name: 'handshake', status: 'done' },
        { name: 'promote', status: 'done' },
        { name: 'verify', status: 'done' },
        { name: 'retire', status: 'done' },
        { name: 'failback', status: 'done' },
      ],
    })
    expect(v.ok).toBe(true)
    expect(v.steps.every((s) => s.state === 'done')).toBe(true)
  })

  // Пустой ответ -- «починки не было», а не сломанный экран.
  it('переживает отсутствие задания', () => {
    const v = repairView({})
    expect(v.done).toBe(false)
    expect(v.steps).toHaveLength(3)
    expect(v.steps.every((s) => s.state === 'pending')).toBe(true)
  })
})
