import { describe, it, expect } from 'vitest'
import { agentReplyText } from '../src/errorText.js'

const FB = 'Роутер не переспросил — попробуйте ещё раз через минуту.'

// Ответ агента (result.output) -- английский технический текст и голые статусы
// («timeout») на экран не выходят: фраза экрана, а русский вывод агента -- после неё.
describe('agentReplyText', () => {
  it('английский вывод -- только запасная фраза', () => {
    expect(agentReplyText({ status: 'error', output: 'awgmgr GET /api/x: HTTP 500' }, FB)).toBe(FB)
  })
  it('русский вывод -- дописывается после фразы', () => {
    expect(agentReplyText({ status: 'error', output: 'Служба не отвечает.' }, FB)).toBe(`${FB} Служба не отвечает.`)
  })
  it('пусто и голый статус -- запасная фраза', () => {
    expect(agentReplyText({ status: 'timeout', output: '' }, FB)).toBe(FB)
    expect(agentReplyText({ status: 'timeout' }, FB)).toBe(FB)
    expect(agentReplyText(null, FB)).toBe(FB)
  })
})
