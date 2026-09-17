import { describe, it, expect } from 'vitest'
import { appMode, WIDE_QUERY } from '../src/mode.js'

describe('appMode', () => {
  it('всё под /dashboard -- веб-управление', () => {
    expect(appMode('/dashboard/')).toBe('web')
    expect(appMode('/dashboard/login')).toBe('web')
    expect(appMode('/dashboard')).toBe('web')
  })

  it('мини-апп и всё прочее -- Telegram', () => {
    expect(appMode('/miniapp/')).toBe('telegram')
    expect(appMode('/')).toBe('telegram')
    expect(appMode('')).toBe('telegram')
    expect(appMode(undefined)).toBe('telegram')
    // Префикс, а не подстрока: /miniapp/dashboard -- не веб-вариант.
    expect(appMode('/miniapp/dashboard')).toBe('telegram')
  })

  it('порог широкой раскладки -- 1024 px', () => {
    expect(WIDE_QUERY).toBe('(min-width: 1024px)')
  })
})
