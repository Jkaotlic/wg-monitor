import { describe, expect, it } from 'vitest'
import { incidentStoryView } from '../src/incidentStory.js'

describe('incidentStoryView', () => {
  it('показывает один упорядоченный рассказ вместо дублирования transitions', () => {
    const view = incidentStoryView({
      story: [
        { at: '2026-11-06T10:00:00Z', kind: 'failure_detected', tone: 'danger', title: 'Обнаружен сбой', detail: 'VPN-туннель перестал проходить проверку' },
        { at: '2026-11-06T10:04:00Z', kind: 'recovered', tone: 'ok', title: 'Связь восстановилась', detail: 'Новая проверка прошла успешно' },
      ],
      transitions: [{ ts: '2026-11-06T10:00:00Z', status: 'fail', label: 'сбой' }],
    })
    expect(view.map((x) => x.kind)).toEqual(['failure_detected', 'recovered'])
    expect(view[0].title).toBe('Обнаружен сбой')
  })

  it('тихо откатывается к прежним переходам со старым backend', () => {
    expect(incidentStoryView({
      transitions: [
        { ts: '2026-11-06T10:00:00Z', status: 'fail', label: 'сбой' },
        { ts: '2026-11-06T10:04:00Z', status: 'ok', label: 'в норме' },
      ],
    })).toEqual([
      { at: '2026-11-06T10:00:00Z', kind: 'transition_fail', tone: 'danger', title: 'сбой' },
      { at: '2026-11-06T10:04:00Z', kind: 'transition_ok', tone: 'ok', title: 'в норме' },
    ])
  })

  it('возвращает пустой рассказ, когда событий нет', () => {
    expect(incidentStoryView(null)).toEqual([])
  })
})
