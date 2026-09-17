import { describe, it, expect } from 'vitest'
import { copyText } from '../src/clipboard.js'

function fakeDoc({ execResult = true } = {}) {
  const log = []
  const doc = {
    body: { appendChild: (el) => log.push(['append', el.value]) },
    createElement: () => ({
      value: '',
      style: {},
      setAttribute: () => {},
      select: () => log.push(['select']),
      remove: () => log.push(['remove']),
    }),
    execCommand: (cmd) => {
      log.push(['exec', cmd])
      return execResult
    },
  }
  return { doc, log }
}

describe('copyText', () => {
  it('через navigator.clipboard, если он есть', async () => {
    const got = []
    const env = { navigator: { clipboard: { writeText: async (v) => got.push(v) } } }
    expect(await copyText('tok-123', env)).toBe(true)
    expect(got).toEqual(['tok-123'])
  })

  it('clipboard отказал -- запасной путь через textarea и execCommand', async () => {
    const { doc, log } = fakeDoc()
    const env = { navigator: { clipboard: { writeText: async () => { throw new Error('denied') } } }, document: doc }
    expect(await copyText('tok-123', env)).toBe(true)
    expect(log).toEqual([['append', 'tok-123'], ['select'], ['exec', 'copy'], ['remove']])
  })

  it('без clipboard и без execCommand -- false, а не исключение', async () => {
    expect(await copyText('x', { navigator: {}, document: {} })).toBe(false)
  })

  it('execCommand вернул false -- false', async () => {
    const { doc } = fakeDoc({ execResult: false })
    expect(await copyText('x', { navigator: {}, document: doc })).toBe(false)
  })

  it('пустое не копируется', async () => {
    expect(await copyText('', { navigator: { clipboard: { writeText: async () => {} } } })).toBe(false)
  })
})
