import { describe, it, expect } from 'vitest'
import { CONFIG_SOURCES_TITLE, configSourceChoices, configSourceTarget } from '../src/configSources.js'

const ok = (n) => ({ status: 'ok', panels: Array.from({ length: n }, (_, i) => ({ id: `p${i}` })) })
const values = (c) => c.map((x) => x.value)

describe('лист «Откуда взять конфиг»', () => {
  it('заголовок без слова «кабинет»', () => {
    expect(CONFIG_SOURCES_TITLE).toBe('Откуда взять конфиг')
  })
  it('владелец без панелей: Amnezia, HideMy, .conf', () => {
    expect(values(configSourceChoices({ isAdmin: false, canImport: true, awg3: ok(0) }))).toEqual(['amnezia', 'hidemy', 'conf'])
  })
  it('админ с панелями: + свой сервер и панель VPN-сервера', () => {
    const c = configSourceChoices({ isAdmin: true, canImport: true, awg3: ok(2) })
    expect(values(c)).toEqual(['amnezia', 'hidemy', 'selfhosted', 'awg3', 'conf'])
    expect(c.find((x) => x.value === 'awg3').label).toBe('Панель VPN-сервера')
  })
  it('допущенный к панели без права на .conf', () => {
    expect(values(configSourceChoices({ isAdmin: false, canImport: false, awg3: ok(1) }))).toEqual(['amnezia', 'hidemy', 'awg3'])
  })
  it('список панелей не загрузился -- пункт с пометкой, а не тишина', () => {
    const c = configSourceChoices({ isAdmin: false, canImport: true, awg3: { status: 'error', panels: [] } })
    expect(c.find((x) => x.value === 'awg3-retry')).toEqual({ value: 'awg3-retry', label: 'Панель VPN-сервера', pill: { tone: 'warn', text: 'не загрузилось — повторить' } })
  })
  it('куда ведёт вариант', () => {
    expect(configSourceTarget('hidemy')).toEqual({ overlay: 'cabinet', params: { tab: 'hidemy' } })
    expect(configSourceTarget('awg3')).toEqual({ overlay: 'cabinet', params: { tab: 'awg3' } })
    expect(configSourceTarget('conf')).toEqual({ overlay: 'confimport' })
    expect(configSourceTarget('awg3-retry')).toBe(null)
  })
})
