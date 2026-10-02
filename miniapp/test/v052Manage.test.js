import { describe, it, expect } from 'vitest'
import { MANAGE_SECTIONS, manageSection, manageAnchors, manageTones, manageSummaries, versionsKnown } from '../src/manage.js'
import { MANAGE_FOCUS } from '../src/nav.js'

const FW = { rows: [{ component: 'firmware', installed: '5.0', available: '5.1' }] }

describe('v0.52: разделы «Настроек»', () => {
  it('четыре раздела в порядке спеки', () => {
    expect(MANAGE_SECTIONS.map((s) => s.title)).toEqual(['Обслуживание', 'Люди и уведомления', 'Роутер и агент', 'Опасное'])
    expect(manageSection('people').chip).toBe('Люди')
  })
  it('фокус старых ссылок ведёт в существующий раздел', () => {
    for (const id of Object.values(MANAGE_FOCUS)) expect(manageSection(id)).toBeTruthy()
  })
  it('чипы: «Опасное» -- только админу', () => {
    expect(manageAnchors({ isAdmin: false }).map((a) => a.label)).toEqual(['Обслуживание', 'Люди', 'Роутер и агент'])
    expect(manageAnchors({ isAdmin: true }).map((a) => a.id)).toEqual(['mg-service', 'mg-people', 'mg-agent', 'mg-danger'])
  })
  it('забота -- у «Обслуживания»: прошивка danger, перезагрузка и старый агент warn', () => {
    expect(manageTones({ versions: FW }).service).toBe('danger')
    expect(manageTones({ showReboot: true }).service).toBe('warn')
    expect(manageTones({ agentReady: false }).service).toBe('warn')
    expect(manageTones({})).toEqual({ service: null, people: null, agent: null, danger: null })
  })
  it('итоговые строки', () => {
    expect(manageSummaries({ showReboot: true }).service).toBe('нужна перезагрузка роутера')
    expect(manageSummaries({ settings: { notify_muted: true } }).people).toBe('уведомления выключены')
    expect(manageSummaries({ settings: { notify_muted: false }, isAdmin: true }).people).toBe('уведомления включены · доступ')
    expect(manageSummaries({ settings: { role: 'operator' } }).agent).toBe('пороги тревог')
    expect(manageSummaries({ settings: { role: 'owner' } }).agent).toBe('панель роутера, пороги тревог')
    expect(manageSummaries({ isAdmin: true }).agent).toBe('панель роутера, пороги тревог, агент')
  })
  it('versionsKnown: известно хоть что-то -- да; пусто или «сведений нет» -- нет', () => {
    expect(versionsKnown(null)).toBe(false)
    expect(versionsKnown({ installed: {}, rows: [] })).toBe(false)
    expect(versionsKnown({ installed: { awgmgr: '2.19.9' }, rows: [] })).toBe(true)
  })
})
