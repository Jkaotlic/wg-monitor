import { describe, it, expect } from 'vitest'
import {
  REVIVE_NOT_CONFIGURED,
  REVIVE_SECRET_NOTE,
  REVIVE_DEFAULT_DAYS,
  REVIVE_EXPIRY_OPTIONS,
  reviveState,
  reviveNotConfiguredLine,
  reviveSheetText,
  reviveFields,
  reviveReady,
  reviveRequestBody,
  reviveErrorText,
  reviveDoneText,
  reviveCancelSheetText,
  reviveCancelDoneText,
} from '../src/revive.js'
import { ApiError } from '../src/api.js'

// Форма -- miniappFleetRouter/miniappFleetRevive (miniapp_fleet.go, цикл 2б).
const FLEET_AT = '2026-09-15T10:00:00Z'
const fleet = (over = {}) => ({ generated_at: FLEET_AT, revive_enabled: true, routers: [], ...over })
const off = (over = {}) => ({
  id: 11, nickname: 'caredns-oldcar', status: 'offline', away: true, last_seen_age_sec: 345600,
  panel_address_known: true, revive: null, ...over,
})
const rv = (over = {}) => ({
  status: 'waiting', expires_at: '2026-10-15T12:00:00Z', attempts: 0,
  last_error_text: '', last_probe_text: '', last_probe_at: '', ...over,
})

// Внутренние имена в тексте для людей.
const INTERNAL = /awgm|root_password|api_key|revive|intent|probe|waiting|running|expired|failed/

describe('оживление: строка роутера', () => {
  it('ждёт: «ждёт роутер · проверка 2 мин назад: не отвечает», отмена есть, оживить нельзя', () => {
    const s = reviveState(off({ revive: rv({ last_probe_at: '2026-09-15T09:58:00Z', last_probe_text: 'не отвечает' }) }), fleet())
    expect(s).toEqual({ tone: 'warn', text: 'ждёт роутер · проверка 2 мин назад: не отвечает', canRevive: false, canCancel: true })
  })

  it('ждёт, опроса ещё не было -- так и сказано', () => {
    expect(reviveState(off({ revive: rv() }), fleet()).text).toBe('ждёт роутер · проверок ещё не было')
  })

  it('ждёт, опрос меньше минуты назад -- «только что»', () => {
    const s = reviveState(off({ revive: rv({ last_probe_at: '2026-09-15T09:59:30Z', last_probe_text: 'не отвечает' }) }), fleet())
    expect(s.text).toBe('ждёт роутер · проверка только что: не отвечает')
  })

  it('ждёт после неудачных попыток -- число и прошлая причина', () => {
    const s = reviveState(
      off({ revive: rv({ attempts: 2, last_error_text: 'установка прервалась', last_probe_at: '2026-09-15T09:57:00Z', last_probe_text: 'отвечает' }) }),
      fleet(),
    )
    expect(s.text).toBe('ждёт роутер · проверка 3 мин назад: отвечает · 2 попытки, прошлая: установка прервалась')
  })

  it('оживляется: без кнопок', () => {
    expect(reviveState(off({ revive: rv({ status: 'running' }) }), fleet())).toEqual({ tone: 'muted', text: 'оживляется…', canRevive: false, canCancel: false })
  })

  it('ожил: зелёная строка, у роутера на связи кнопки нет', () => {
    expect(reviveState(off({ status: 'online', away: false, revive: rv({ status: 'done' }) }), fleet())).toEqual({ tone: 'ok', text: 'ожил', canRevive: false, canCancel: false })
  })

  it('не вышло: причина словами, можно поставить заново', () => {
    expect(reviveState(off({ revive: rv({ status: 'failed', last_error_text: 'пароль не подошёл' }) }), fleet()))
      .toEqual({ tone: 'danger', text: 'не вышло: пароль не подошёл', canRevive: true, canCancel: false })
    expect(reviveState(off({ revive: rv({ status: 'failed' }) }), fleet()).text).toBe('не вышло: причина не названа')
  })

  it('срок истёк: можно поставить заново', () => {
    expect(reviveState(off({ revive: rv({ status: 'expired' }) }), fleet())).toEqual({ tone: 'warn', text: 'срок истёк', canRevive: true, canCancel: false })
  })

  it('отменено или не ставили: строки нет, кнопка -- только у роутера не на связи', () => {
    expect(reviveState(off({ revive: rv({ status: 'cancelled' }) }), fleet())).toEqual({ tone: 'ok', text: '', canRevive: true, canCancel: false })
    expect(reviveState(off(), fleet()).canRevive).toBe(true)
    expect(reviveState(off({ status: 'online', away: false }), fleet()).canRevive).toBe(false)
  })

  it('оживление не настроено: ни «Оживить», ни «Отменить», строка состояния остаётся', () => {
    const s = reviveState(off({ revive: rv({ last_probe_at: '2026-09-15T09:58:00Z', last_probe_text: 'не отвечает' }) }), fleet({ revive_enabled: false }))
    expect(s.canRevive).toBe(false)
    expect(s.canCancel).toBe(false)
    expect(s.text).toContain('ждёт роутер')
    expect(reviveState(off(), fleet({ revive_enabled: false })).canRevive).toBe(false)
    // Старый сервер без поля -- тоже «нельзя».
    expect(reviveState(off(), { generated_at: FLEET_AT }).canRevive).toBe(false)
  })

  it('«не настроено на сервере» -- одна строка экрана, только если сервер сказал false и есть кого оживлять', () => {
    expect(reviveNotConfiguredLine(fleet({ revive_enabled: false, routers: [off()] }))).toBe(REVIVE_NOT_CONFIGURED)
    expect(reviveNotConfiguredLine(fleet({ revive_enabled: false, routers: [off({ status: 'online', away: false })] }))).toBe('')
    expect(reviveNotConfiguredLine(fleet({ revive_enabled: true, routers: [off()] }))).toBe('')
    expect(reviveNotConfiguredLine({ routers: [off()] })).toBe('')
    expect(REVIVE_NOT_CONFIGURED).toBe('Оживление агента не настроено на сервере.')
  })
})

describe('оживление: лист', () => {
  it('поля: пароли -- type=password, адрес панели только когда он не записан, срок по умолчанию 30', () => {
    const known = reviveFields(off())
    expect(known.map((f) => f.name)).toEqual(['root_password', 'awgm_login', 'awgm_password', 'awgm_api_key', 'expires_days'])
    for (const name of ['root_password', 'awgm_password', 'awgm_api_key']) {
      expect(known.find((f) => f.name === name).type).toBe('password')
    }
    const days = known.find((f) => f.name === 'expires_days')
    expect(days.type).toBe('select')
    expect(days.initial).toBe(REVIVE_DEFAULT_DAYS)
    // Пре-флайт 15.09: варианты 7/14/30, по порядку.
    expect(days.options.map((o) => o.value)).toEqual(['7', '14', '30'])
    expect(REVIVE_EXPIRY_OPTIONS.map((o) => o.label)).toEqual(['7 дней', '14 дней', '30 дней'])
    // Вход в панель -- необязательный, и подпись это говорит.
    for (const name of ['awgm_login', 'awgm_password', 'awgm_api_key']) {
      expect(known.find((f) => f.name === name).label).toContain('необязательно')
    }
    expect(known.find((f) => f.name === 'root_password').label).not.toContain('необязательно')
    const bronya = reviveFields(off({ nickname: 'bronya', panel_address_known: false }))
    expect(bronya.map((f) => f.name)).toContain('awgm_url')
    // Поле без признака (старый сервер) -- адрес не спрашиваем.
    expect(reviveFields(off({ panel_address_known: undefined })).map((f) => f.name)).not.toContain('awgm_url')
    for (const f of bronya) {
      expect(f.label).not.toMatch(INTERNAL)
      expect(f.label).toMatch(/[А-Яа-яЁё]/)
    }
  })

  // Пре-флайт 15.09: без пароля root переустановка отказывает, вход в панель
  // его дополняет, но не заменяет -- кнопка без root не горит.
  it('готовность: пароль root обязателен, вход в панель его не заменяет; у bronya ещё и адрес', () => {
    const ready = reviveReady(off())
    expect(ready({})).toBe(false)
    expect(ready({ root_password: 'x' })).toBe(true)
    expect(ready({ root_password: '   ' })).toBe(false)
    expect(ready({ awgm_login: 'admin' })).toBe(false)
    expect(ready({ awgm_login: 'admin', awgm_password: 'p' })).toBe(false)
    expect(ready({ awgm_api_key: 'k' })).toBe(false)
    expect(ready({ root_password: 'x', awgm_login: 'admin', awgm_password: 'p', awgm_api_key: 'k' })).toBe(true)
    const bronya = reviveReady(off({ panel_address_known: false }))
    expect(bronya({ root_password: 'x' })).toBe(false)
    expect(bronya({ root_password: 'x', awgm_url: '  ' })).toBe(false)
    expect(bronya({ root_password: 'x', awgm_url: 'https://192.168.1.1' })).toBe(true)
  })

  it('тело запроса: пустых полей нет, пароль не обрезан, срок числом, адрес -- только когда спрашивали', () => {
    expect(reviveRequestBody({ root_password: ' pa ss ', awgm_login: '', expires_days: '7' }, 'Bronya', off()))
      .toEqual({ confirm: 'Bronya', expires_days: 7, root_password: ' pa ss ' })
    expect(reviveRequestBody({ root_password: 'r', awgm_login: ' admin ', awgm_password: 'p', awgm_url: 'https://x' }, 'car', off()))
      .toEqual({ confirm: 'car', expires_days: 30, root_password: 'r', awgm_login: 'admin', awgm_password: 'p' })
    expect(reviveRequestBody({ root_password: 'r', awgm_api_key: ' k ', awgm_url: ' https://192.168.1.1 ', expires_days: '14' }, 'bronya', off({ panel_address_known: false })))
      .toEqual({ confirm: 'bronya', expires_days: 14, root_password: 'r', awgm_api_key: 'k', awgm_url: 'https://192.168.1.1' })
  })

  it('текст листа: имя роутера, без внутренних имён; про адрес -- только у bronya; предупреждение про пароль', () => {
    const t = reviveSheetText(off())
    expect(t.title).toBe('Оживить агент на «caredns-oldcar»?')
    expect(t.body).not.toMatch(INTERNAL)
    expect(t.body).not.toContain('адрес панели')
    expect(t.body).toContain('Нужен пароль root роутера')
    expect(reviveSheetText(off({ nickname: 'bronya', panel_address_known: false })).body).toContain('Адрес панели у роутера не записан')
    expect(REVIVE_SECRET_NOTE).toBe('Пароль хранится на сервере зашифрованным до оживления, потом стирается.')
  })

  it('отказы -- фразы экрана; сырой код и английский текст не показываются', () => {
    const cases = {
      confirm_mismatch: 'Имя роутера набрано неверно — оживление не поставлено.',
      revive_disabled: REVIVE_NOT_CONFIGURED,
      no_awgm_url: 'У роутера не записан адрес панели — укажите его.',
      invalid_awgm_url: 'Адрес панели должен начинаться с http:// или https://.',
      awgm_url_already_set: 'Адрес панели у роутера уже записан — закройте лист и откройте заново.',
      no_credentials: 'Нужен пароль root роутера.',
      agent_alive: 'Агент на роутере отвечает — оживлять нечего.',
      not_found: 'Роутер не найден — закройте экран и откройте заново.',
      // Пре-флайт 15.09: отмена или повтор во время переустановки, роутер
      // удалён между чтением парка и постановкой.
      revive_running: 'Оживление уже идёт — дождитесь итога.',
      router_not_found: 'Роутер не найден.',
    }
    for (const [code, text] of Object.entries(cases)) {
      expect(reviveErrorText(new ApiError(400, code, `x failed: 400`, 'server'))).toBe(text)
      expect(text).not.toMatch(INTERNAL)
    }
    expect(reviveErrorText(new ApiError(401, 'unauthorized', 'x', 'sign in required'))).toBe('Сессия истекла — откройте приложение заново.')
    expect(reviveErrorText(new ApiError(500, 'internal', 'x', 'Не удалось поставить оживление.'))).toBe('Не удалось поставить оживление.')
    expect(reviveErrorText(new ApiError(502, 'unknown', 'x', 'Bad Gateway'))).toBe('')
  })

  it('итог: ждёт -- «переустановится, когда выйдет на связь» с датой; запущено сразу -- так и сказано', () => {
    expect(reviveDoneText({ status: 'waiting', expires_at: '2026-10-15T12:00:00Z' }, 'bronya'))
      .toBe('Оживление «bronya» поставлено: агент переустановится, когда роутер выйдет на связь. Ждём до 15.10.2026.')
    expect(reviveDoneText({ status: 'running', expires_at: '2026-10-15T12:00:00Z' }, 'bronya'))
      .toBe('Оживление «bronya» запущено: роутер на связи, агент ставится заново.')
  })

  it('отмена: лист и итог словами', () => {
    const t = reviveCancelSheetText(off({ nickname: 'bronya' }))
    expect(t.title).toBe('Отменить оживление агента на «bronya»?')
    expect(t.body).toBe('Сервер перестанет ждать роутер и сотрёт пароль. Поставить оживление можно будет заново.')
    expect(reviveCancelDoneText({ cleared: true }, 'bronya')).toBe('Оживление «bronya» отменено, пароль стёрт.')
    expect(reviveCancelDoneText({ cleared: false }, 'bronya')).toBe('Отменять было нечего: оживление «bronya» уже завершилось или снято.')
  })
})
