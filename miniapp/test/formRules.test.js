import { describe, it, expect } from 'vitest'
import { validNickname, validAgentVersion, validHttpURL, NICKNAME_RULE } from '../src/formRules.js'

describe('имя роутера -- как на сервере ^[a-z][a-z0-9_-]{1,15}$', () => {
  it('годные', () => {
    for (const v of ['dacha-1', 'ab', 'car_2', 'abcdefghijklmnop', '  home  ']) expect(validNickname(v), v).toBe(true)
  })
  it('негодные', () => {
    for (const v of ['a', 'Dacha', '1dacha', '-dacha', 'дача', 'abcdefghijklmnopq', 'da cha', '', null, undefined]) {
      expect(validNickname(v), String(v)).toBe(false)
    }
  })
  it('правило сказано словами', () => {
    expect(NICKNAME_RULE).toBe('Имя роутера: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.')
  })
})

describe('версия агента', () => {
  it('vN.N.N и vN.N.N-rcN', () => {
    for (const v of ['v0.36.0', 'v1.2.3-rc4', ' v10.0.12 ']) expect(validAgentVersion(v), v).toBe(true)
  })
  it('прочее -- нет', () => {
    for (const v of ['0.36.0', 'v0.36', 'latest', 'v1.2.3-beta', '', null]) expect(validAgentVersion(v), String(v)).toBe(false)
  })
})

describe('адрес http(s)', () => {
  it('годные', () => {
    for (const v of ['https://router.example.com', 'http://198.51.100.7:8080/', ' https://router.example.com/path ']) {
      expect(validHttpURL(v), v).toBe(true)
    }
  })
  it('негодные', () => {
    for (const v of ['router.example.com', 'ftp://router.example.com', 'https://', 'javascript:alert(1)', '', null]) {
      expect(validHttpURL(v), String(v)).toBe(false)
    }
  })
})
