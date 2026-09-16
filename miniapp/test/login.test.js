import { describe, it, expect } from 'vitest'
import { ApiError } from '../src/api.js'
import {
  takeHashToken,
  redeemFromHash,
  loginErrorText,
  bootFailure,
  SESSION_EXPIRED_TEXT,
  ADMIN_NOT_CONFIGURED_TEXT,
} from '../src/login.js'

function fakeLocation(url) {
  const u = new URL(url, 'https://wg.example.com')
  return { pathname: u.pathname, search: u.search, hash: u.hash }
}

function fakeHistory(log) {
  return { replaceState: (_s, _t, url) => log.push(['replace', url]) }
}

describe('токен из ссылки', () => {
  it('достаётся и стирается из адреса, query сохраняется', () => {
    const log = []
    const token = takeHashToken(fakeLocation('/dashboard/login?router=3#token=abc%2Bdef'), fakeHistory(log))
    expect(token).toBe('abc+def')
    expect(log).toEqual([['replace', '/dashboard/login?router=3']])
  })

  it('без токена адрес не трогается', () => {
    const log = []
    expect(takeHashToken(fakeLocation('/dashboard/login#other=1'), fakeHistory(log))).toBe('')
    expect(takeHashToken(fakeLocation('/dashboard/'), fakeHistory(log))).toBe('')
    expect(log).toEqual([])
  })

  it('хэш стирается ДО запроса -- токен не остаётся в истории при ошибке', async () => {
    const log = []
    const p = redeemFromHash({
      location: fakeLocation('/dashboard/login#token=t1'),
      history: fakeHistory(log),
      redeem: (t) => {
        log.push(['redeem', t])
        return Promise.reject(new ApiError(401, 'unauthorized', 'x', 'Ссылка больше не действует — попросите новую.'))
      },
    })
    await expect(p).rejects.toBeInstanceOf(ApiError)
    expect(log).toEqual([['replace', '/dashboard/login'], ['redeem', 't1']])
  })

  it('без токена -- null, запроса нет', () => {
    const log = []
    const p = redeemFromHash({ location: fakeLocation('/dashboard/login'), history: fakeHistory(log), redeem: () => log.push('redeem') })
    expect(p).toBe(null)
    expect(log).toEqual([])
  })
})

describe('тексты ошибок входа', () => {
  const e = (status, code, serverMessage = '') => new ApiError(status, code, 'x', serverMessage)

  it('токен не подошёл -- своя фраза, английская сервера не показывается', () => {
    expect(loginErrorText(e(401, 'unauthorized', 'unauthorized'), 'token')).toBe('Токен не подошёл')
  })

  it('мёртвая ссылка -- фраза сервера как есть', () => {
    expect(loginErrorText(e(401, 'unauthorized', 'Ссылка больше не действует — попросите новую.'), 'link')).toBe('Ссылка больше не действует — попросите новую.')
    expect(loginErrorText(e(401, 'unauthorized', ''), 'link')).toBe('Ссылка больше не действует — попросите новую.')
  })

  it('429 -- фраза сервера, без неё -- своя', () => {
    expect(loginErrorText(e(429, 'rate_limited', 'Слишком много попыток. Попробуйте через 12 с.'), 'token')).toBe('Слишком много попыток. Попробуйте через 12 с.')
    expect(loginErrorText(e(429, 'rate_limited', ''), 'link')).toBe('Слишком много попыток, подождите минуту')
  })

  it('сеть -- «Сервер не отвечает», прочее -- общая фраза', () => {
    expect(loginErrorText(new TypeError('Failed to fetch'), 'token')).toBe('Сервер не отвечает')
    expect(loginErrorText(e(500, 'internal'), 'token')).toBe('Не получилось войти. Попробуйте ещё раз.')
  })
})

describe('bootFailure', () => {
  const e = (status, code) => new ApiError(status, code, 'x')

  it('web: 401 -- экран входа без заметки', () => {
    expect(bootFailure(e(401, 'unauthorized'), 'web')).toEqual({ status: 'login', notice: '' })
  })

  it('web: администратор не задан -- экран входа с объяснением', () => {
    expect(bootFailure(e(401, 'admin_not_configured'), 'web')).toEqual({ status: 'login', notice: ADMIN_NOT_CONFIGURED_TEXT })
  })

  it('web: сеть и 5xx -- «Сервер не отвечает»', () => {
    expect(bootFailure(new TypeError('Failed to fetch'), 'web')).toEqual({ status: 'down', notice: '' })
    expect(bootFailure(e(502, 'unknown'), 'web')).toEqual({ status: 'down', notice: '' })
  })

  it('telegram: как было -- одна ошибка на всё', () => {
    expect(bootFailure(e(401, 'unauthorized'), 'telegram')).toEqual({ status: 'error', notice: '' })
    expect(bootFailure(new TypeError('x'), 'telegram')).toEqual({ status: 'error', notice: '' })
  })

  it('тексты', () => {
    expect(SESSION_EXPIRED_TEXT).toBe('Сессия закончилась — войдите снова')
    expect(ADMIN_NOT_CONFIGURED_TEXT).toBe('На сервере не задан администратор — вход в веб-управление невозможен')
  })
})
