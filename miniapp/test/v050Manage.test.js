import { describe, it, expect } from 'vitest'
import { navReducer, MANAGE_FOCUS } from '../src/nav.js'
import { navFromURL } from '../src/navUrl.js'

describe('старые ссылки и возврат раскрывают нужную группу (Review Focus 5)', () => {
  it('словарь', () => {
    expect(MANAGE_FOCUS).toEqual({ settings: 'agent', admin: 'service', packages: 'service', dnsreset: 'service', agentcfg: 'agent', agentconn: 'agent' })
  })
  it('?open=admin / ?open=settings', () => {
    expect(navFromURL('?router=7&open=admin', [7])).toMatchObject({ routerID: 7, tab: 'manage', manageFocus: 'service' })
    expect(navFromURL('?router=7&open=settings', [7])).toMatchObject({ tab: 'manage', manageFocus: 'agent' })
    expect('manageFocus' in navFromURL('?router=7&tab=manage', [7])).toBe(false)
  })
  it('возврат из «Пакетов» -- «Починить»; смена вкладки и роутера фокус снимают', () => {
    const back = navReducer({ routerID: 7, tab: 'manage', overlay: 'packages', sheet: null }, { type: 'overlay', overlay: 'manage' })
    expect(back).toMatchObject({ tab: 'manage', overlay: null, manageFocus: 'service' })
    expect('manageFocus' in navReducer(back, { type: 'tab', tab: 'diag' })).toBe(false)
    expect('manageFocus' in navReducer(back, { type: 'router', id: 8 })).toBe(false)
  })
  it('повторный переход в ту же группу -- новый номер фокуса', () => {
    const a = navReducer({ routerID: 7, tab: 'manage', overlay: 'packages', sheet: null }, { type: 'overlay', overlay: 'manage' })
    const b = navReducer({ ...a, overlay: 'packages' }, { type: 'overlay', overlay: 'manage' })
    expect(a.manageFocus).toBe('service')
    expect(b.manageFocus).toBe('service')
    expect(b.manageFocusSeq).toBeGreaterThan(a.manageFocusSeq)
  })

  it('возврат «Хода работы» во вкладку -- форма состояния прежняя', () => {
    const layer = { routerID: 3, tab: 'router', overlay: 'job', overlayParams: { jobId: 'j', returnTo: 'manage' }, sheet: null }
    expect(navReducer(layer, { type: 'back' })).toEqual({ routerID: 3, tab: 'manage', overlay: null, sheet: null })
  })
})
