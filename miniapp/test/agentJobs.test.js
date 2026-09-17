import { describe, it, expect } from 'vitest'
import { ApiError } from '../src/api.js'
import { jobTitle } from '../src/jobSteps.js'
import {
  JOB_SECRET_NOTE,
  reinstallAllowed,
  reinstallSheetText,
  reinstallFields,
  reinstallReady,
  reinstallRequestBody,
  reinstallJobTitle,
  backendURLCheck,
  repointSheetText,
  repointFields,
  repointReady,
  repointRequestBody,
  repointJobTitle,
  jobStartErrorText,
} from '../src/agentJobs.js'

const HOME = { id: 22, nickname: 'home', status: 'online', away: false }

describe('переустановка агента сейчас', () => {
  it('только роутеру на связи', () => {
    expect(reinstallAllowed(HOME)).toBe(true)
    expect(reinstallAllowed({ ...HOME, status: 'alert' })).toBe(true)
    expect(reinstallAllowed({ ...HOME, away: true })).toBe(false)
    expect(reinstallAllowed({ nickname: 'x', status: 'sleeping' })).toBe(false)
    expect(reinstallAllowed(null)).toBe(false)
  })

  it('лист называет роутер и что будет', () => {
    const t = reinstallSheetText(HOME)
    expect(t.title).toBe('Переустановить агент на «home» сейчас?')
    expect(t.body).toContain('Нужен пароль root')
    expect(JOB_SECRET_NOTE).toBe('Пароли уходят на сервер один раз и не сохраняются.')
  })

  it('поля: пароли -- паролями, версия с подсказкой', () => {
    const f = reinstallFields()
    expect(f.map((x) => [x.name, x.type])).toEqual([
      ['root_password', 'password'],
      ['awgm_login', 'text'],
      ['awgm_password', 'password'],
      ['awgm_api_key', 'password'],
      ['version', 'text'],
    ])
    const version = f.find((x) => x.name === 'version')
    expect(version.hint({ version: '' })).toBe('Пусто — последняя версия.')
    expect(version.hint({ version: '0.3' })).toBe('Версия пишется так: v0.36.0.')
  })

  it('готово: пароль root не из одних пробелов и версия пустая или годная', () => {
    expect(reinstallReady({ root_password: '', version: '' })).toBe(false)
    expect(reinstallReady({ root_password: '   ', version: '' })).toBe(false)
    expect(reinstallReady({ root_password: 'S3 cret', version: '' })).toBe(true)
    expect(reinstallReady({ root_password: 'x', version: '0.35.0' })).toBe(true)
    expect(reinstallReady({ root_password: 'x', version: 'last' })).toBe(false)
  })

  it('тело -- все поля контракта; пароли не обрезаются, логин и ключ -- да', () => {
    expect(
      reinstallRequestBody({ root_password: ' pw ', awgm_login: ' admin ', awgm_password: ' p ', awgm_api_key: ' k ', version: '0.35.0' }, 'home'),
    ).toEqual({ root_password: ' pw ', awgm_login: 'admin', awgm_password: ' p ', awgm_api_key: 'k', version: 'v0.35.0', confirm: 'home' })
    expect(reinstallRequestBody({ root_password: 'pw' }, 'home')).toEqual({
      root_password: 'pw', awgm_login: '', awgm_password: '', awgm_api_key: '', version: '', confirm: 'home',
    })
  })

  it('версия -- по правилу formRules: -beta не годится', () => {
    expect(reinstallReady({ root_password: 'x', version: 'v0.35.0-beta' })).toBe(false)
    expect(reinstallReady({ root_password: 'x', version: 'v0.35.0-rc2' })).toBe(true)
  })

  it('заголовок «Хода работы» -- из jobSteps.jobTitle', () => {
    expect(reinstallJobTitle(HOME)).toBe(jobTitle('repair_reinstall', 'home'))
    expect(reinstallJobTitle(HOME)).toBe('Переустановка агента на «home»')
  })
})

describe('перенаправление агента', () => {
  it('адрес: пусто -- текущий, только https с именем хоста', () => {
    expect(backendURLCheck('')).toEqual({ ok: true, hint: 'Пусто — текущий публичный адрес этого сервера.' })
    expect(backendURLCheck(' https://wg2.example.com ')).toEqual({ ok: true, hint: '' })
    expect(backendURLCheck('http://wg2.example.com')).toEqual({ ok: false, hint: 'Нужен адрес с https://, например https://wg.example.com.' })
    expect(backendURLCheck('wg2.example.com')).toEqual({ ok: false, hint: 'Нужен адрес с https://, например https://wg.example.com.' })
    expect(backendURLCheck('https://')).toEqual({ ok: false, hint: 'Нужен адрес с https://, например https://wg.example.com.' })
  })

  it('лист предупреждает словами спеки', () => {
    const t = repointSheetText(HOME)
    expect(t.title).toBe('Перенаправить агента «home» на другой сервер?')
    expect(t.body).toContain('Агент начнёт отправлять отчёты на другой сервер. Этот сервер перестанет его видеть.')
  })

  it('поля и готовность', () => {
    expect(repointFields().map((x) => [x.name, x.type])).toEqual([
      ['root_password', 'password'],
      ['new_backend_url', 'text'],
      ['awgm_login', 'text'],
      ['awgm_password', 'password'],
      ['awgm_api_key', 'password'],
    ])
    expect(repointReady({ root_password: 'x', new_backend_url: '' })).toBe(true)
    expect(repointReady({ root_password: 'x', new_backend_url: 'http://a' })).toBe(false)
    expect(repointReady({ root_password: '', new_backend_url: 'https://a.example.com' })).toBe(false)
  })

  it('тело -- все поля контракта, адрес обрезан', () => {
    expect(repointRequestBody({ root_password: 'pw', new_backend_url: ' https://wg2.example.com ' }, 'home')).toEqual({
      root_password: 'pw', new_backend_url: 'https://wg2.example.com', awgm_login: '', awgm_password: '', awgm_api_key: '', confirm: 'home',
    })
    expect(repointJobTitle(HOME)).toBe('Перенаправление агента «home»')
  })
})

describe('отказы запуска задания', () => {
  it('сообщение сервера -- как есть; 401 и сеть -- своими словами', () => {
    expect(jobStartErrorText(new ApiError(409, 'router_offline', 'x', 'Роутер не на связи.'))).toBe('Роутер не на связи.')
    expect(jobStartErrorText(new ApiError(401, 'unauthorized', 'x', 'sign in required'))).toBe('Сессия истекла — войдите заново.')
    expect(jobStartErrorText(new ApiError(502, 'unknown', 'x', ''))).toBe('')
    expect(jobStartErrorText(new Error('Failed to fetch'))).toBe('')
  })
})
