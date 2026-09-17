import { describe, it, expect } from 'vitest'
import {
  CONNECTION_KEYS,
  CONNECTION_GROUPS,
  CONNECTION_TEXTS,
  connectionFormValues,
  connectionSelectOptions,
  validateConnection,
  connectionRequestBody,
  connectionChanged,
  connectionErrorText,
} from '../src/agentConnection.js'
import { ApiError } from '../src/api.js'

const RESP = {
  awgm_url: 'https://router.example.com', awgm_auth: 'web', ssh_host: '198.51.100.7', ssh_port: 222,
  ssh_user: 'root', deploy_mode: 'awgm', arch: 'arm64', ring: 'stable', expected_mac: 'aa:bb:cc:dd:ee:ff',
}

describe('группы', () => {
  it('четыре группы спеки и все девять полей', () => {
    expect(CONNECTION_GROUPS.map((g) => g.title)).toEqual(['Панель awg-manager', 'SSH', 'Раскатка', 'Проверка роутера'])
    expect(CONNECTION_GROUPS.flatMap((g) => g.fields.map((f) => f.key))).toEqual(CONNECTION_KEYS)
    expect(CONNECTION_KEYS).toEqual(['awgm_url', 'awgm_auth', 'ssh_host', 'ssh_port', 'ssh_user', 'deploy_mode', 'arch', 'ring', 'expected_mac'])
    expect(CONNECTION_TEXTS.keepNote).toBe('Пустое поле оставляет прежнее значение.')
  })
})

describe('значения формы', () => {
  it('строки; порт 0 или null -- пусто', () => {
    expect(connectionFormValues(RESP)).toEqual({ ...RESP, ssh_port: '222' })
    expect(connectionFormValues({ ssh_port: 0 }).ssh_port).toBe('')
    expect(connectionFormValues(null).awgm_url).toBe('')
  })

  it('список выбора держит и незнакомое текущее значение', () => {
    const mode = CONNECTION_GROUPS[2].fields.find((f) => f.key === 'deploy_mode')
    const values = connectionSelectOptions(mode, 'awgm').map((o) => o.value)
    // Значение уже есть -- «не задано» не предлагаем: очистить поле нельзя,
    // сервер оставит прежнее.
    expect(values).toEqual(['awgm', 'pull', 'ssh', 'deferred-awgm'])
    expect(connectionSelectOptions(mode, '').map((o) => o.value)).toEqual(['', 'awgm', 'pull', 'ssh', 'deferred-awgm'])
    const custom = connectionSelectOptions(mode, 'legacy')
    expect(custom[custom.length - 1]).toEqual({ value: 'legacy', label: 'legacy' })
    const auth = CONNECTION_GROUPS[0].fields.find((f) => f.key === 'awgm_auth')
    expect(connectionSelectOptions(auth, 'web').map((o) => o.value)).toEqual(['web', 'api-key', 'none'])
  })
})

describe('проверка', () => {
  const ok = connectionFormValues(RESP)
  it('годная форма', () => {
    expect(validateConnection(ok)).toBe('')
    expect(validateConnection({ ...ok, awgm_url: '', ssh_port: '', expected_mac: '' })).toBe('')
  })
  it('адрес, порт, MAC', () => {
    expect(validateConnection({ ...ok, awgm_url: 'router.example.com' })).toBe('Адрес панели должен начинаться с https:// или http://')
    expect(validateConnection({ ...ok, ssh_port: '70000' })).toBe('Порт SSH — число от 1 до 65535')
    expect(validateConnection({ ...ok, ssh_port: '22a' })).toBe('Порт SSH — число от 1 до 65535')
    expect(validateConnection({ ...ok, expected_mac: 'aa:bb' })).toBe('MAC пишется так: aa:bb:cc:dd:ee:ff')
  })
})

describe('тело PUT', () => {
  it('обрезано, порт числом, пустой порт -- 0', () => {
    expect(connectionRequestBody({ ...connectionFormValues(RESP), ssh_host: ' 198.51.100.8 ' })).toEqual({ ...RESP, ssh_host: '198.51.100.8' })
    expect(connectionRequestBody({ ...connectionFormValues(RESP), ssh_port: '' }).ssh_port).toBe(0)
  })

  it('изменилось ли что-то -- по телу, а не по пробелам', () => {
    const a = connectionFormValues(RESP)
    expect(connectionChanged(a, { ...a, ssh_user: ' root ' })).toBe(false)
    expect(connectionChanged(a, { ...a, ring: 'rc' })).toBe(true)
  })
})

describe('ошибки', () => {
  it('сервер, затем своя фраза, затем общее', () => {
    expect(connectionErrorText(new ApiError(400, 'invalid_arch', 'x', 'Архитектура не поддерживается'))).toBe('Архитектура не поддерживается')
    expect(connectionErrorText(new ApiError(400, 'invalid_arch', 'x'))).toBe('Архитектура: arm64 или mipsle')
    expect(connectionErrorText(new ApiError(400, 'invalid_awgm_url', 'x'))).toBe('Адрес панели должен начинаться с https:// или http://')
    expect(connectionErrorText(new ApiError(400, 'invalid_kind', 'x'))).toBe('Неизвестный тип роутера')
    expect(connectionErrorText(new Error('net'))).toBe('Не удалось сохранить. Попробуйте ещё раз.')
  })
})
