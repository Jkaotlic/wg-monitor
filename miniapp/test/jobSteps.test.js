import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { JOB_STATES, STEP_STATUSES, STEP_NAMES, stepLabel, jobTitle, jobView } from '../src/jobSteps.js'

// Имена и значения -- контракт с Go (provision/steps.go, provision/job.go).
// Тест читает исходники: новый шаг на сервере без подписи здесь -- красный CI.
const stepsGo = readFileSync(new URL('../../internal/backend/provision/steps.go', import.meta.url), 'utf8')
const jobGo = readFileSync(new URL('../../internal/backend/provision/job.go', import.meta.url), 'utf8')

describe('сверка с сервером', () => {
  it('у каждого шага provision.Template есть подпись', () => {
    const names = [...stepsGo.matchAll(/^\s*Step\w+\s*=\s*"([a-z_]+)"/gm)].map((m) => m[1])
    expect(names).toHaveLength(10)
    expect([...STEP_NAMES].sort()).toEqual([...names].sort())
  })

  it('состояния задания и шагов -- как в job.go', () => {
    expect([...jobGo.matchAll(/JobState\s*=\s*"(\w+)"/g)].map((m) => m[1])).toEqual(JOB_STATES)
    expect([...jobGo.matchAll(/StepStatus\s*=\s*"(\w+)"/g)].map((m) => m[1])).toEqual(STEP_STATUSES)
  })
})

describe('подписи шагов', () => {
  it('по-русски', () => {
    expect(stepLabel('terminal_connected')).toBe('Вход в терминал роутера')
    expect(stepLabel('arch_detected')).toBe('Определение архитектуры роутера')
    expect(stepLabel('downloading')).toBe('Скачивание агента')
    expect(stepLabel('checksum_ok')).toBe('Проверка подписи и контрольной суммы')
    expect(stepLabel('config_written')).toBe('Запись настроек агента')
    expect(stepLabel('init_installed')).toBe('Установка автозапуска')
    expect(stepLabel('service_started')).toBe('Запуск агента')
    expect(stepLabel('backend_url_rewritten')).toBe('Смена адреса сервера')
    expect(stepLabel('service_restarted')).toBe('Перезапуск агента')
    expect(stepLabel('verify_online')).toBe('Агент выходит на связь')
  })

  it('незнакомый шаг -- как есть, не пустота', () => {
    expect(stepLabel('future_step')).toBe('future_step')
    expect(stepLabel(undefined)).toBe('')
  })
})

describe('jobTitle', () => {
  it('по виду задания', () => {
    expect(jobTitle('provision', 'dacha-1')).toBe('Установка агента на «dacha-1»')
    expect(jobTitle('repair_reinstall', 'dacha-1')).toBe('Переустановка агента на «dacha-1»')
    expect(jobTitle('repair_repoint', 'dacha-1')).toBe('Перенаправление агента «dacha-1»')
    expect(jobTitle('нечто', 'x')).toBe('Установка агента на «x»')
  })
})

const steps = (statuses) =>
  ['terminal_connected', 'arch_detected', 'downloading'].map((name, i) => ({ name, status: statuses[i], detail: '' }))

describe('jobView', () => {
  it('идёт: заголовок по виду, шаги с подписями, неизвестный статус -- «ждёт»', () => {
    const v = jobView({ kind: 'provision', state: 'running', steps: steps(['done', 'active', 'weird']), hint: 'не показывать', tail: 'не показывать' })
    expect(v.headline).toBe('Установка агента идёт')
    expect(v.tone).toBe('running')
    expect(v.finished).toBe(false)
    expect(v.steps.map((s) => [s.label, s.status])).toEqual([
      ['Вход в терминал роутера', 'done'],
      ['Определение архитектуры роутера', 'active'],
      ['Скачивание агента', 'pending'],
    ])
    expect(v.hint).toBe('')
    expect(v.tail).toBe('')
  })

  it('провал: шаг словами, hint и tail', () => {
    const v = jobView({
      kind: 'repair_reinstall',
      state: 'failed',
      steps: steps(['done', 'done', 'failed']),
      hint: 'Роутер не скачал агента: проверьте интернет на роутере.',
      tail: 'wget: bad address',
    })
    expect(v.headline).toBe('Не получилось: Скачивание агента')
    expect(v.tone).toBe('bad')
    expect(v.finished).toBe(true)
    expect(v.success).toBe(false)
    expect(v.hint).toBe('Роутер не скачал агента: проверьте интернет на роутере.')
    expect(v.tail).toBe('wget: bad address')
  })

  it('успех: версия в заголовке, роутер для «Открыть роутер»', () => {
    const v = jobView({ kind: 'provision', state: 'success', steps: steps(['done', 'done', 'done']), version: 'v0.36.0', nickname: 'dacha-1', router_id: 9 })
    expect(v.headline).toBe('Агент установлен и на связи · v0.36.0')
    expect(v.tone).toBe('ok')
    expect(v.success).toBe(true)
    expect(v.routerID).toBe(9)
    expect(v.nickname).toBe('dacha-1')
    expect(jobView({ kind: 'repair_repoint', state: 'success', steps: [] }).headline).toBe('Агент перенаправлен и на связи')
  })

  it('пустое и мусор не роняют экран', () => {
    const v = jobView(null)
    expect(v.steps).toEqual([])
    expect(v.routerID).toBe(null)
    expect(v.finished).toBe(false)
  })
})
