import { describe, it, expect } from 'vitest'
import { replaceView, stepTitle } from '../src/replace.js'

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
