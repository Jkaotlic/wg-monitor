import { describe, it, expect } from 'vitest'
import {
  PROVISION_PATHS,
  AGENT_KINDS,
  AWGM_AUTH_OPTIONS,
  PROVISION_SECRET_NOTE,
  WIZARD_SECRET_KEYS,
  TOKEN_TEXTS,
  wizardSteps,
  initialWizardValues,
  clearSecrets,
  stepError,
  nextStep,
  prevStep,
  stepPosition,
  provisionSummary,
  provisionRequestBody,
  provisionErrorStep,
  provisionErrorText,
  tokenResultView,
} from '../src/provisionWizard.js'
import { ApiError } from '../src/api.js'
import { NICKNAME_RULE } from '../src/formRules.js'

const install = (over = {}) => ({
  ...initialWizardValues(),
  path: 'install',
  nickname: 'dacha-1',
  awgmURL: 'https://router.example.com',
  rootPassword: ' root pass ',
  awgmLogin: ' admin ',
  awgmPassword: 'panel pass',
  ...over,
})

describe('варианты', () => {
  it('два пути, два типа, три способа входа с реальными значениями сервера', () => {
    expect(PROVISION_PATHS.map((p) => [p.value, p.title])).toEqual([
      ['install', 'Установить агента сейчас'],
      ['token', 'Только выдать токен'],
    ])
    expect(AGENT_KINDS.map((k) => [k.value, k.title])).toEqual([
      ['static', 'Дома'],
      ['mobile', 'В машине'],
    ])
    expect(AWGM_AUTH_OPTIONS.map((o) => o.value)).toEqual(['web', 'api-key', 'none'])
    expect(PROVISION_SECRET_NOTE).toBe('Пароль уходит на сервер один раз и не сохраняется.')
  })

  it('начальные значения: путь не выбран, дома, вход в веб, версия -- последняя', () => {
    expect(initialWizardValues()).toEqual({
      path: '', nickname: '', agentKind: 'static', awgmURL: '', awgmAuth: 'web',
      rootPassword: '', awgmLogin: '', awgmPassword: '', awgmAPIKey: '', version: '',
    })
  })
})

describe('шаги', () => {
  it('установка -- четыре шага, токен -- три (без доступа)', () => {
    expect(wizardSteps('install')).toEqual(['path', 'router', 'access', 'confirm'])
    expect(wizardSteps('token')).toEqual(['path', 'router', 'confirm'])
    expect(wizardSteps('')).toEqual(['path', 'router', 'access', 'confirm'])
  })

  it('вперёд и назад по пути', () => {
    expect(nextStep('token', 'router')).toBe('confirm')
    expect(nextStep('install', 'router')).toBe('access')
    expect(nextStep('install', 'confirm')).toBe(null)
    expect(prevStep('token', 'confirm')).toBe('router')
    expect(prevStep('install', 'confirm')).toBe('access')
    expect(prevStep('install', 'path')).toBe(null)
  })

  it('позиция для «Шаг N из M»', () => {
    expect(stepPosition('install', 'access')).toEqual({ index: 3, total: 4 })
    expect(stepPosition('token', 'confirm')).toEqual({ index: 3, total: 3 })
  })
})

describe('проверка шагов', () => {
  it('путь обязателен', () => {
    expect(stepError('path', initialWizardValues())).toBe('Выберите, как добавить роутер.')
    expect(stepError('path', { ...initialWizardValues(), path: 'token' })).toBe('')
  })

  it('имя -- правило сервера', () => {
    expect(stepError('router', install({ nickname: 'Дача' }))).toBe(NICKNAME_RULE)
    expect(stepError('router', install({ nickname: 'dacha-1' }))).toBe('')
    expect(stepError('router', install({ agentKind: 'boat' }))).toBe('Выберите, где стоит роутер.')
  })

  it('доступ: адрес, пароль root, вход в панель, версия', () => {
    expect(stepError('access', install({ awgmURL: 'router.example.com' }))).toBe('Нужен адрес панели awg-manager: https://…')
    expect(stepError('access', install({ rootPassword: '   ' }))).toBe('Нужен пароль root')
    expect(stepError('access', install({ awgmLogin: '' }))).toBe('Для входа в веб нужны логин и пароль панели.')
    expect(stepError('access', install({ awgmAuth: 'api-key', awgmAPIKey: ' ' }))).toBe('Нужен ключ API панели.')
    expect(stepError('access', install({ awgmAuth: 'none', awgmLogin: '', awgmPassword: '' }))).toBe('')
    expect(stepError('access', install({ awgmAuth: 'other' }))).toBe('Выберите способ входа в панель.')
    expect(stepError('access', install({ version: 'latest' }))).toBe('Версия пишется так: v0.36.0. Пусто — последняя.')
    expect(stepError('access', install({ version: 'v0.36.0' }))).toBe('')
    expect(stepError('access', install())).toBe('')
  })

  it('подтверждение само не проверяется -- это делает набор ника', () => {
    expect(stepError('confirm', initialWizardValues())).toBe('')
  })
})

describe('тело запроса', () => {
  it('токен: только имя, тип и набранное', () => {
    expect(provisionRequestBody({ ...initialWizardValues(), path: 'token', nickname: ' dacha-1 ', agentKind: 'mobile', rootPassword: 'лишнее' }, 'dacha-1')).toEqual({
      kind: 'register', nickname: 'dacha-1', agent_kind: 'mobile', confirm: 'dacha-1',
    })
  })

  it('установка со входом в веб: пароли не обрезаются, логин и адрес -- да', () => {
    expect(provisionRequestBody(install({ version: ' v0.36.0 ' }), 'dacha-1')).toEqual({
      kind: 'provision', nickname: 'dacha-1', agent_kind: 'static', confirm: 'dacha-1',
      awgm_url: 'https://router.example.com', awgm_auth: 'web', root_password: ' root pass ', version: 'v0.36.0',
      awgm_login: 'admin', awgm_password: 'panel pass',
    })
  })

  it('установка с ключом: логина и пароля панели в теле нет', () => {
    const body = provisionRequestBody(install({ awgmAuth: 'api-key', awgmAPIKey: ' key-1 ' }), 'dacha-1')
    expect(body.awgm_api_key).toBe('key-1')
    expect('awgm_login' in body).toBe(false)
    expect('awgm_password' in body).toBe(false)
  })

  it('без входа: ни одного секрета панели', () => {
    const body = provisionRequestBody(install({ awgmAuth: 'none', awgmAPIKey: 'x' }), 'dacha-1')
    expect(Object.keys(body).sort()).toEqual(['agent_kind', 'awgm_auth', 'awgm_url', 'confirm', 'kind', 'nickname', 'root_password', 'version'])
  })

  it('стирание секретов', () => {
    const cleared = clearSecrets(install({ awgmAPIKey: 'k' }))
    for (const k of WIZARD_SECRET_KEYS) expect(cleared[k]).toBe('')
    expect(WIZARD_SECRET_KEYS).toEqual(['rootPassword', 'awgmPassword', 'awgmAPIKey'])
    expect(cleared.nickname).toBe('dacha-1')
    expect(cleared.awgmLogin).toBe(' admin ')
  })
})

describe('сводка', () => {
  it('установка: пароли не показываются', () => {
    const rows = provisionSummary(install())
    expect(rows).toEqual([
      { label: 'Способ', value: 'Установить агента сейчас' },
      { label: 'Имя', value: 'dacha-1' },
      { label: 'Где стоит', value: 'Дома' },
      { label: 'Панель awg-manager', value: 'https://router.example.com' },
      { label: 'Вход в панель', value: 'Вход в веб (логин и пароль)' },
      { label: 'Пароль root', value: 'введён' },
      { label: 'Версия агента', value: 'последняя' },
    ])
    expect(JSON.stringify(rows)).not.toContain('root pass')
    expect(JSON.stringify(rows)).not.toContain('panel pass')
    // После отправки пароли стёрты -- сводка не врёт, что он есть.
    expect(provisionSummary(clearSecrets(install()))[5]).toEqual({ label: 'Пароль root', value: 'не введён' })
  })

  it('токен: без доступа', () => {
    expect(provisionSummary({ ...initialWizardValues(), path: 'token', nickname: 'car', agentKind: 'mobile' })).toEqual([
      { label: 'Способ', value: 'Только выдать токен' },
      { label: 'Имя', value: 'car' },
      { label: 'Где стоит', value: 'В машине' },
    ])
  })
})

describe('ошибки', () => {
  const e = (code, msg = '') => new ApiError(400, code, 'x', msg)

  it('отказ возвращает на шаг, где поле', () => {
    expect(provisionErrorStep(e('invalid_nickname'), 'install')).toBe('router')
    expect(provisionErrorStep(e('nickname_taken'), 'token')).toBe('router')
    expect(provisionErrorStep(e('invalid_awgm_url'), 'install')).toBe('access')
    expect(provisionErrorStep(e('provision_already_running'), 'install')).toBe('router')
    expect(provisionErrorStep(e('root_password_required'), 'install')).toBe('access')
    expect(provisionErrorStep(e('checksums_failed'), 'install')).toBe('access')
    expect(provisionErrorStep(e('no_awgm_url'), 'token')).toBe('confirm')
    expect(provisionErrorStep(e('provision_not_configured'), 'install')).toBe('confirm')
    expect(provisionErrorStep(new Error('net'), 'token')).toBe('confirm')
  })

  it('слова: сервер, затем своя фраза, затем общее', () => {
    expect(provisionErrorText(e('invalid_nickname', 'Имя роутера: латиница, цифры и дефис'))).toBe('Имя роутера: латиница, цифры и дефис')
    expect(provisionErrorText(e('confirm_mismatch'))).toBe('Подтверждение не совпало')
    expect(provisionErrorText(e('provision_not_configured'))).toBe('Установка агентов на сервере не настроена')
    expect(provisionErrorText(e('root_password_required'))).toBe('Нужен пароль root')
    expect(provisionErrorText(e('no_awgm_url'))).toBe('Нужен адрес панели awg-manager')
    expect(provisionErrorText(e('provision_already_running'))).toBe('Установка на этот роутер уже идёт')
    expect(provisionErrorText(e('latest_version_failed'))).toBe('Не удалось узнать последнюю версию — повторите через минуту')
    expect(provisionErrorText(e('checksums_failed'))).toBe('Не удалось скачать контрольные суммы релиза')
    expect(provisionErrorText(e('no_public_base_url'))).toBe('У сервера не задан публичный адрес — агенту некуда отправлять отчёты')
    expect(provisionErrorText(new Error('net'))).toBe('Не получилось. Попробуйте ещё раз.')
  })
})

describe('экран токена', () => {
  it('поля ответа register', () => {
    expect(tokenResultView({ nickname: 'car', raw_token: 'tok', backend_url: 'https://wg.example.com', install_command: 'sh install.sh' })).toEqual({
      nickname: 'car', token: 'tok', backendURL: 'https://wg.example.com', installCommand: 'sh install.sh',
    })
    expect(tokenResultView({}).installCommand).toBe('')
  })

  it('слова', () => {
    expect(TOKEN_TEXTS.once).toBe('Токен показывается один раз: закроете экран — увидеть его снова будет нельзя.')
    expect(TOKEN_TEXTS.after).toBe('Владельца роутеру назначают потом — во вкладке «Управление» → «Доступ».')
  })
})

describe('версия в мастере -- как в листе', () => {
  it('0.36.0 дописывается до v0.36.0 и проходит проверку', () => {
    expect(stepError('access', install({ version: '0.36.0' }))).toBe('')
    expect(provisionRequestBody(install({ version: ' 0.36.0 ' }), 'dacha-1').version).toBe('v0.36.0')
    expect(provisionSummary(install({ version: '0.36.0' }))[6]).toEqual({ label: 'Версия агента', value: 'v0.36.0' })
  })
})
