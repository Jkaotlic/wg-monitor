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

// v0.54: лесенка автопочинки -- пять шагов задания, три стадии на экране.
describe('repairView: лесенка автопочинки', () => {
  const ladder = (st) => ['failover', 'restart', 'reissue', 'recreate', 'failback'].map((name, i) => ({ name, status: st[i] ?? 'pending' }))

  it('пять шагов сворачиваются в failover / raise / failback', () => {
    const v = repairView({ state: 'running', running: true, steps: ladder(['done', 'active']) })
    expect(v.steps.map((s) => s.key)).toEqual(['failover', 'raise', 'failback'])
    expect(v.steps.map((s) => s.label)).toEqual(['Увожу трафик на запасной VPN-туннель', 'Поднимаю VPN-туннель', 'Возвращаю всё на место'])
    expect(v.steps.map((s) => s.state)).toEqual(['done', 'active', 'pending'])
  })

  it('подстрока -- detail активного шага', () => {
    const steps = ladder(['done', 'active'])
    steps[1].detail = 'перезапускаю'
    expect(repairView({ state: 'running', running: true, steps }).steps[1].sub).toBe('перезапускаю')
  })

  it('перезапуск помог: остальные шаги skipped, стадия done, подстрока -- последнего не пропущенного', () => {
    const steps = ladder(['done', 'done', 'skipped', 'skipped', 'done'])
    steps[1].detail = 'тот же конфиг, связь есть'
    const v = repairView({ state: 'success', steps })
    expect(v.steps[1].state).toBe('done')
    expect(v.steps[1].sub).toBe('тот же конфиг, связь есть')
    expect(v.ok).toBe(true)
    expect(v.note).toBe('')
  })

  it('skipped не делает стадию проваленной; провал перезапуска при удаче пересоздания -- не провал', () => {
    const v = repairView({ state: 'success', steps: ladder(['done', 'failed', 'done', 'skipped', 'done']) })
    expect(v.steps[1].state).toBe('done')
    expect(v.note).toBe('')
  })

  it('все ступени провалились -- стадия failed, причина в note', () => {
    const steps = ladder(['done', 'failed', 'failed', 'failed', 'skipped'])
    steps[3].detail = 'место на сервере кончилось'
    const v = repairView({ state: 'failed', steps })
    expect(v.steps[1].state).toBe('failed')
    expect(v.steps[1].sub).toBe('место на сервере кончилось')
    expect(v.note).toBe('место на сервере кончилось')
    expect(v.done).toBe(true)
    expect(v.ok).toBe(false)
  })

  it('провал лесенки -- «что делать» из подсказки задания, отдельно от причины', () => {
    const steps = ladder(['done', 'failed', 'failed', 'skipped', 'skipped'])
    steps[2].detail = 'источник не выдал конфиг'
    const v = repairView({ state: 'failed', hint: 'обновите ключ «Amnezia Premium» во вкладке «Управление»', steps })
    expect(v.note).toBe('источник не выдал конфиг')
    expect(v.action).toBe('обновите ключ «Amnezia Premium» во вкладке «Управление»')
  })

  it('«что делать» -- только у законченного провала лесенки', () => {
    expect(repairView({ state: 'success', hint: 'x', steps: ladder(['done', 'done', 'skipped', 'skipped', 'done']) }).action).toBe('')
    expect(repairView({ state: 'running', running: true, hint: 'x', steps: ladder(['done', 'active']) }).action).toBe('')
    expect(repairView({ state: 'failed', steps: ladder(['done', 'failed', 'failed', 'failed', 'skipped']) }).action).toBe('')
  })

  it('стадия увода показывает, что сделано: резерв упал -- «трафик и так идёт через …»', () => {
    const steps = ladder(['done', 'active'])
    steps[0].detail = 'трафик и так идёт через «Работа»'
    expect(repairView({ state: 'running', running: true, steps }).steps[0].sub).toBe('трафик и так идёт через «Работа»')
  })

  it('провал посреди идущего задания -- ещё active, пока есть куда идти', () => {
    const v = repairView({ state: 'running', running: true, steps: ladder(['done', 'failed', 'pending']) })
    expect(v.steps[1].state).toBe('active')
    expect(v.note).toBe('')
  })

  it('пропущенный возврат: стадия skipped, а не failed', () => {
    const v = repairView({ state: 'success', steps: ladder(['skipped', 'done', 'skipped', 'skipped', 'skipped']) })
    expect(v.steps.map((s) => s.state)).toEqual(['skipped', 'done', 'skipped'])
  })

  it('старое задание (шаги мастера, без restart) -- прежняя свёртка', () => {
    const v = repairView({ state: 'running', running: true, steps: [{ name: 'failover', status: 'done' }, { name: 'issue', status: 'active' }, { name: 'failback', status: 'pending' }] })
    expect(v.steps.map((s) => s.key)).toEqual(['failover', 'reissue', 'failback'])
    expect(v.steps[1].label).toBe('Выпускаю новый конфиг и поднимаю VPN-туннель')
    expect(v.steps[1].state).toBe('active')
  })

  it('skipped в старой свёртке не рушит стадию', () => {
    const v = repairView({ state: 'success', steps: [{ name: 'failover', status: 'skipped' }, { name: 'issue', status: 'done' }, { name: 'retire', status: 'skipped' }, { name: 'failback', status: 'skipped' }] })
    expect(v.steps[1].state).toBe('done')
  })
})
