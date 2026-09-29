// v0.48: оговорки обновления агента (agent_update_warning) -- фразы из
// короткого набора сервера (agent_update_verdict.go), склеенные «; ».
// Одна и та же фраза под каждым из семи роутеров -- шум: Парк говорит её
// один раз над списком и перечисляет, кого касается. «Агент слишком
// старый» -- не оговорка к обновлению, а причина, почему кнопки «Обновить»
// нет вовсе: она остаётся на карточке своего роутера.
import { describe, it, expect } from 'vitest'
import { fleetWarningNotes, cardWarning } from '../src/fleetAdmin.js'

const SPACE = 'старая проверка места: нужно ≈10% раздела /opt свободно'
const REPO = 'проверяет адрес загрузки: он должен совпасть с адресом бэкенда в настройках агента'
const OLD = 'агент слишком старый — нужна переустановка'
const row = (name, warning) => ({ name, warning })

describe('fleetWarningNotes', () => {
  it('одна фраза -- одна строка со всеми роутерами, в порядке первого появления', () => {
    const notes = fleetWarningNotes([
      row('broken', `${SPACE}; ${REPO}`),
      row('work', REPO),
      row('car', `${SPACE}; ${REPO}.`),
      row('fresh', ''),
    ])
    expect(notes).toEqual([
      { text: SPACE, names: ['broken', 'car'] },
      { text: REPO, names: ['broken', 'work', 'car'] },
    ])
  })

  it('«слишком старый» в общую строку не идёт', () => {
    expect(fleetWarningNotes([row('antique', OLD)])).toEqual([])
  })
})

describe('cardWarning', () => {
  it('на карточке -- только «слишком старый»; прочее -- метка', () => {
    expect(cardWarning(row('antique', OLD))).toEqual({ text: OLD, tagged: false })
    expect(cardWarning(row('car', `${SPACE}; ${REPO}`))).toEqual({ text: '', tagged: true })
    expect(cardWarning(row('fresh', ''))).toEqual({ text: '', tagged: false })
  })
})
