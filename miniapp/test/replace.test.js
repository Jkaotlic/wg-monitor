import { describe, it, expect } from 'vitest'
import { replaceView, stepTitle, startErrorText } from '../src/replace.js'

describe('stepTitle', () => {
  // Человеку показывают не имена шагов движка, а то, что происходит на
  // роутере: он смотрит на этот список, пока линия меняется под ним.
  it('каждый шаг назван последствием', () => {
    expect(stepTitle('issue')).toBe('Выпускаем конфиг у провайдера')
    expect(stepTitle('import')).toBe('Кладём новый туннель рядом с прежним')
    expect(stepTitle('handshake')).toBe('Ждём рукопожатия новой линии')
    expect(stepTitle('promote')).toBe('Переводим политику на новый туннель')
    expect(stepTitle('verify')).toBe('Проверяем, каким адресом видно снаружи')
    expect(stepTitle('retire')).toBe('Выключаем прежний туннель')
  })

  it('незнакомый шаг показывается как есть, а не теряется', () => {
    expect(stepTitle('нечто')).toBe('нечто')
  })
})

describe('replaceView', () => {
  const job = (state, steps, hint = '') => ({ job_id: 'j1', state, hint, running: state === 'running', steps })

  it('идущая замена показывает, на каком шаге стоим', () => {
    const v = replaceView(job('running', [
      { name: 'issue', status: 'done', detail: 'конфиг получен' },
      { name: 'import', status: 'active', detail: 'кладём конфиг' },
      { name: 'handshake', status: 'pending' },
    ]))
    expect(v.running).toBe(true)
    expect(v.current.title).toBe('Кладём новый туннель рядом с прежним')
    expect(v.steps).toHaveLength(3)
    expect(v.steps[0].tone).toBe('ok')
    expect(v.steps[1].tone).toBe('sig')
    expect(v.steps[2].tone).toBe('off')
  })

  it('успех говорит, чем всё кончилось', () => {
    const v = replaceView(job('success', [{ name: 'retire', status: 'done' }], 'готово: политика идёт через amnezia_nl'))
    expect(v.running).toBe(false)
    expect(v.tone).toBe('ok')
    expect(v.headline).toContain('amnezia_nl')
  })

  // Провал обязан называть и причину, и то, что откат уже сделан: человек
  // читает этот экран, чтобы понять, в каком состоянии остался роутер.
  it('провал называет причину и состояние отката', () => {
    const v = replaceView(job('failed', [
      { name: 'verify', status: 'failed', detail: 'снаружи виден тот же адрес' },
    ], 'снаружи виден тот же адрес (203.0.113.7): подмены нет. Откат: политика возвращена прежнему туннелю'))
    expect(v.tone).toBe('danger')
    expect(v.headline).toContain('тот же адрес')
    expect(v.rollback).toContain('политика возвращена')
  })

  it('замен не было — экран показывает форму, а не пустой список', () => {
    expect(replaceView(null).idle).toBe(true)
    expect(replaceView({}).idle).toBe(true)
  })
})

// Отказ сервера доезжал до человека в виде «/routers/5/replace failed: 400»:
// путь запроса и число. Причина при этом известна -- код ошибки лежит в
// ответе, -- и не сказать её значит заставить оператора гадать там, где
// сервер уже всё объяснил.
describe('startErrorText', () => {
  const apiErr = (status, code) => Object.assign(new Error(`/x failed: ${status}`), { status, code })

  it('старый агент -- называет причину и что сделать', () => {
    const text = startErrorText(apiErr(400, 'agent_too_old'))
    expect(text).toMatch(/агент/i)
    expect(text).toMatch(/обнов/i)
    expect(text).not.toMatch(/failed/)
  })

  it('замена уже идёт', () => {
    expect(startErrorText(apiErr(409, 'already_running'))).toMatch(/уже идёт/i)
  })

  it('сервер без кабинетов -- это настройка сервера, а не вина роутера', () => {
    expect(startErrorText(apiErr(503, 'not_configured'))).toMatch(/не настроен/i)
  })

  it('незнакомый код -- честное «не знаю», но без внутренностей запроса', () => {
    const text = startErrorText(apiErr(500, 'boom'))
    expect(text).not.toMatch(/\/x failed/)
    expect(text).toMatch(/500/)
  })

  it('не-ответ сервера (сеть оборвалась) тоже читается', () => {
    expect(startErrorText(new Error('Failed to fetch'))).toMatch(/связ|сет/i)
  })
})
