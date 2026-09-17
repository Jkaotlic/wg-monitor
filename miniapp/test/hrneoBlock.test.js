import { describe, it, expect } from 'vitest'
import {
  HRNEO_TEXTS,
  parseHrneoInventory,
  hrneoState,
  hrneoStatusLine,
  mayControlHrneo,
  hrneoActions,
  hrneoSheet,
  hrneoRuleRows,
  inventoryNote,
} from '../src/hrneoBlock.js'

// Форма ответа hrneo_inventory -- wire.HRNeoInventory (pkg/wire/routing.go).
const INV = {
  status: { installed: true, running: true },
  rules: [
    { id: 'r1', name: 'youtube', enabled: true, bind: 'nwg1', domains: ['youtube.example.com', 'ytimg.example.com'] },
    { id: 'r2', name: 'office', enabled: false, policy_name: 'VPN', routes: ['203.0.113.0/24'] },
    { id: '', name: '', bind: 'policy:Home', manual_domains: ['example.com'] },
  ],
}
const SNAP = { tunnels: [{ id: 'nwg1', name: 'amsterdam', iface: 'nwg1' }], hr_neo: { installed: true, running: false } }

describe('состояние', () => {
  it('разбор ответа агента', () => {
    expect(parseHrneoInventory(JSON.stringify(INV))).toEqual({ installed: true, running: true, rules: INV.rules })
    expect(parseHrneoInventory(JSON.stringify({ status: { installed: false } }))).toEqual({ installed: false, running: false, rules: [] })
    expect(parseHrneoInventory('{"rules":[]}')).toBe(null)
    expect(parseHrneoInventory('not json')).toBe(null)
    expect(parseHrneoInventory('')).toBe(null)
  })

  it('ответ агента важнее снимка; снимок -- запасной путь', () => {
    const inventory = parseHrneoInventory(JSON.stringify(INV))
    expect(hrneoState({ inventory, snapshot: SNAP })).toEqual({ known: true, installed: true, running: true, source: 'inventory' })
    expect(hrneoState({ inventory: null, snapshot: SNAP })).toEqual({ known: true, installed: true, running: false, source: 'snapshot' })
    expect(hrneoState({})).toEqual({ known: false, installed: false, running: false, source: 'none' })
  })

  it('строка состояния', () => {
    expect(hrneoStatusLine({ known: true, installed: true, running: true })).toEqual({ tone: 'ok', chip: 'работает', text: 'Установлен и запущен: правила по имени сайта действуют.' })
    expect(hrneoStatusLine({ known: true, installed: true, running: false })).toEqual({
      tone: 'danger',
      chip: 'остановлен',
      text: 'Установлен, но остановлен: правила по имени сайта не работают, пока его не запустят.',
    })
    expect(hrneoStatusLine({ known: true, installed: false, running: false })).toEqual({ tone: 'muted', chip: 'не установлен', text: 'HydraRoute Neo на роутере не установлен.' })
    expect(hrneoStatusLine({ known: false })).toEqual({ tone: 'muted', chip: 'неизвестно', text: 'Роутер пока не сказал, установлен ли HydraRoute Neo.' })
  })
})

describe('действия и права', () => {
  const up = { known: true, installed: true, running: true }
  const down = { known: true, installed: true, running: false }

  it('запуск и остановка -- владелец и админ, перезапуск -- как в Настройках', () => {
    expect(mayControlHrneo('owner')).toBe(true)
    expect(mayControlHrneo('admin')).toBe(true)
    expect(mayControlHrneo('operator')).toBe(false)
    expect(hrneoActions(up, 'owner')).toEqual([
      { name: 'hrneo', label: 'Перезапустить', danger: false },
      { name: 'hrneo_stop', label: 'Остановить', danger: true },
    ])
    expect(hrneoActions(up, 'operator')).toEqual([{ name: 'hrneo', label: 'Перезапустить', danger: false }])
    expect(hrneoActions(up, '')).toEqual([])
    expect(hrneoActions(down, 'admin')).toEqual([{ name: 'hrneo_start', label: 'Запустить', danger: false }])
    expect(hrneoActions(down, 'operator')).toEqual([])
    expect(hrneoActions({ known: true, installed: false, running: false }, 'admin')).toEqual([])
    expect(hrneoActions({ known: false }, 'admin')).toEqual([])
  })

  it('листы: остановка предупреждает, запуск -- нет', () => {
    const onResult = () => {}
    const stop = hrneoSheet({ routerID: 7, name: 'hrneo_stop', onResult })
    expect(stop).toMatchObject({
      routerID: 7,
      action: 'service_restart',
      args: { name: 'hrneo_stop' },
      title: 'Остановить HydraRoute Neo?',
      buttonLabel: 'Остановить',
      commandLabel: 'остановка HydraRoute Neo',
      danger: true,
      onResult,
    })
    expect(stop.body).toContain('Правила по имени сайта перестанут работать до запуска.')
    expect(hrneoSheet({ routerID: 7, name: 'hrneo_start', asleep: true })).toMatchObject({
      action: 'service_restart',
      args: { name: 'hrneo_start' },
      title: 'Запустить HydraRoute Neo?',
      buttonLabel: 'Запустить',
      commandLabel: 'запуск HydraRoute Neo',
      danger: false,
      asleep: true,
    })
    expect(hrneoSheet({ routerID: 7, name: 'hrneo' })).toMatchObject({ args: { name: 'hrneo' }, title: 'Перезапустить HydraRoute Neo?' })
    expect(() => hrneoSheet({ routerID: 7, name: 'router' })).toThrow()
  })
})

describe('правила', () => {
  it('строки: сколько сайтов и сетей, куда ведёт, выключено ли', () => {
    const inventory = parseHrneoInventory(JSON.stringify(INV))
    expect(hrneoRuleRows(inventory, SNAP)).toEqual([
      { id: 'r1', title: 'youtube', sub: '2 сайта · через «amsterdam»' },
      { id: 'r2', title: 'office', sub: '1 адрес сети · общий набор «VPN» · выключено' },
      { id: 'rule-2', title: 'правило без имени', sub: '1 сайт · общий набор «Home»' },
    ])
    expect(hrneoRuleRows(null, SNAP)).toEqual([])
  })

  it('заметка под состоянием', () => {
    expect(inventoryNote({ inventory: { installed: true, running: true, rules: [] } })).toBe(HRNEO_TEXTS.noRules)
    expect(inventoryNote({ inventory: parseHrneoInventory(JSON.stringify(INV)) })).toBe('')
    expect(inventoryNote({ inventory: { installed: false, running: false, rules: [] } })).toBe('')
    expect(inventoryNote({ busy: true })).toBe(HRNEO_TEXTS.loading)
    expect(inventoryNote({ result: { status: 'err', output: 'unknown action: hrneo_inventory' } })).toBe(HRNEO_TEXTS.oldAgent)
    expect(inventoryNote({ result: { status: 'err', output: 'boom' } })).toBe(HRNEO_TEXTS.failed)
    expect(inventoryNote({ error: 'нет связи' })).toBe(HRNEO_TEXTS.failed)
    expect(inventoryNote({})).toBe('')
  })

  it('словарь: имя движка полностью', () => {
    expect(HRNEO_TEXTS.title).toBe('HydraRoute Neo — движок умной раздельной маршрутизации')
    for (const t of Object.values(HRNEO_TEXTS)) expect(t).not.toMatch(/HR[- ]?Neo|HR-Нео/)
  })
})
