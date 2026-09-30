import { describe, it, expect } from 'vitest'
import { manageAnchors, manageSummaries, manageTones, versionsKnown } from '../src/manage.js'
import { navReducer, MANAGE_FOCUS } from '../src/nav.js'
import { navFromURL } from '../src/navUrl.js'

describe('чипы-якоря (спека п. 3.1)', () => {
  const labels = (o) => manageAnchors(o).map((a) => a.label)
  it('админ -- те же четыре: у «Доступа» своего чипа нет (он в группе «Настроек»)', () => expect(labels({ canRepair: true, isAdmin: true })).toEqual(['Роутер', 'Версии', 'Починить', 'Настройки']))
  it('владелец -- то же', () => expect(labels({ canRepair: true, isAdmin: false })).toEqual(['Роутер', 'Версии', 'Починить', 'Настройки']))
  it('без права обслуживания -- без «Починить»', () => expect(labels({ canRepair: false, isAdmin: false })).toEqual(['Роутер', 'Версии', 'Настройки']))
})

describe('тон свёрнутой группы: забота раскрывает и красит', () => {
  const FW = { rows: [{ component: 'firmware', available: '5.03', installed: '5.02' }], installed: {} }
  const SW = { rows: [{ component: 'awgmgr', available: '2.20.0', installed: '2.19.9' }], installed: {} }
  it('прошивка -- danger у «Версий»', () => expect(manageTones({ versions: FW }).versions).toBe('danger'))
  it('обычная новость -- без тона', () => expect(manageTones({ versions: SW }).versions).toBe(null))
  it('перезагрузка -- warn у «Починить»', () => expect(manageTones({ showReboot: true }).repair).toBe('warn'))
  it('старый агент -- warn у «Починить»', () => expect(manageTones({ agentReady: false }).repair).toBe('warn'))
  it('спокойно -- ни у кого', () => expect(manageTones({ versions: null, showReboot: false, agentReady: true })).toEqual({ versions: null, repair: null, settings: null }))
})

describe('итоговые строки групп', () => {
  const V = (rows = []) => ({ installed: { awgmgr: '2.19.9' }, rows })
  it('версии', () => {
    expect(manageSummaries({ settings: { agent_version: 'v0.47.0' }, versions: null }).versions).toBe('версии ещё не получены')
    expect(manageSummaries({ settings: { agent_version: 'v0.47.0' }, versions: V() }).versions).toBe('агент v0.47.0 · обновлений нет')
    const two = V([{ component: 'awgmgr', available: '2.20.0', installed: '2.19.9' }, { component: 'hrneo', available: '3.1', installed: '3.0' }])
    expect(manageSummaries({ settings: { agent_version: 'v0.47.0' }, versions: two }).versions).toBe('агент v0.47.0 · 2 обновления')
  })
  it('починить: перезагрузка важнее всего, затем старый агент', () => {
    expect(manageSummaries({ showReboot: true, agentReady: false }).repair).toBe('нужна перезагрузка роутера')
    expect(manageSummaries({ agentReady: false }).repair).toBe('агент старый — обслуживание после его обновления')
    expect(manageSummaries({ agentReady: true, isAdmin: true }).repair).toBe('службы, пакеты Entware, сброс DNS')
    expect(manageSummaries({ agentReady: true }).repair).toBe('службы и пакеты Entware')
  })
  it('настройки', () => {
    expect(manageSummaries({ isAdmin: true }).settings).toBe('пороги тревог, агент, доступ')
    expect(manageSummaries({}).settings).toBe('пороги тревог')
  })
  it('versionsKnown: три «сведений нет» -- не известно', () => {
    expect(versionsKnown(null)).toBe(false)
    expect(versionsKnown({ installed: {} })).toBe(false)
    expect(versionsKnown({ installed: { kmod: '1.0.2' } })).toBe(true)
  })
})

describe('старые ссылки и возврат раскрывают нужную группу (Review Focus 5)', () => {
  it('словарь', () => {
    expect(MANAGE_FOCUS).toEqual({ settings: 'router', admin: 'repair', packages: 'repair', dnsreset: 'repair', agentcfg: 'settings', agentconn: 'settings' })
  })
  it('?open=admin / ?open=settings', () => {
    expect(navFromURL('?router=7&open=admin', [7])).toMatchObject({ routerID: 7, tab: 'manage', manageFocus: 'repair' })
    expect(navFromURL('?router=7&open=settings', [7])).toMatchObject({ tab: 'manage', manageFocus: 'router' })
    expect('manageFocus' in navFromURL('?router=7&tab=manage', [7])).toBe(false)
  })
  it('возврат из «Пакетов» -- «Починить»; смена вкладки и роутера фокус снимают', () => {
    const back = navReducer({ routerID: 7, tab: 'manage', overlay: 'packages', sheet: null }, { type: 'overlay', overlay: 'manage' })
    expect(back).toMatchObject({ tab: 'manage', overlay: null, manageFocus: 'repair' })
    expect('manageFocus' in navReducer(back, { type: 'tab', tab: 'diag' })).toBe(false)
    expect('manageFocus' in navReducer(back, { type: 'router', id: 8 })).toBe(false)
  })
  it('повторный переход в ту же группу -- новый номер фокуса', () => {
    const a = navReducer({ routerID: 7, tab: 'manage', overlay: 'packages', sheet: null }, { type: 'overlay', overlay: 'manage' })
    const b = navReducer({ ...a, overlay: 'packages' }, { type: 'overlay', overlay: 'manage' })
    expect(a.manageFocus).toBe('repair')
    expect(b.manageFocus).toBe('repair')
    expect(b.manageFocusSeq).toBeGreaterThan(a.manageFocusSeq)
  })

  it('возврат «Хода работы» во вкладку -- форма состояния прежняя', () => {
    const layer = { routerID: 3, tab: 'router', overlay: 'job', overlayParams: { jobId: 'j', returnTo: 'manage' }, sheet: null }
    expect(navReducer(layer, { type: 'back' })).toEqual({ routerID: 3, tab: 'manage', overlay: null, sheet: null })
  })
})
