// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ bodies: [], reply: null, pending: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  startProvision: (body) => {
    mocks.bodies.push(JSON.parse(JSON.stringify(body)))
    if (mocks.pending) return mocks.pending
    return mocks.reply instanceof Error ? Promise.reject(mocks.reply) : Promise.resolve(mocks.reply)
  },
}))

const { ProvisionWizard } = await import('../src/screens/ProvisionWizard.jsx')
const { ApiError } = await import('../src/api.js')

const ROOT_PW = 'R00t pass-7xq'
const KEY = 'key-9zz'

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const choice = (root, title) => [...root.querySelectorAll('.choice')].find((b) => b.querySelector('.choice-title').textContent === title)
const progress = (root) => root.querySelector('.wizard-progress').textContent
const errorText = (root) => root.querySelector('.wizard-error')?.textContent ?? ''

async function click(el) {
  await act(async () => el.click())
  await flush()
}

async function fill(root, id, value) {
  const el = root.querySelector(`#${id}`)
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  })
}

async function mount() {
  const calls = { closed: 0, started: [], registered: 0, busy: [] }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <ProvisionWizard
        backLabel="Обслуживание"
        onClose={() => calls.closed++}
        onStarted={(arg) => calls.started.push(arg)}
        onRegistered={() => calls.registered++}
        onBusy={(b) => calls.busy.push(b)}
      />,
      root,
    )
  })
  return { root, calls }
}
const cleanup = (root) => { render(null, root); root.remove() }

async function toConfirm(root, { path, nick = 'dacha-1', kind = 'Дома' }) {
  await click(choice(root, path))
  await click(button(root, 'Дальше'))
  await fill(root, 'wizard-nickname', nick)
  await click(choice(root, kind))
  await click(button(root, 'Дальше'))
}

beforeEach(() => {
  mocks.bodies = []
  mocks.reply = null
  mocks.pending = null
})

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('мастер «Добавить роутер»', () => {
  it('шаг 1: без выбора дальше не пускает; путь «токен» -- три шага', async () => {
    const { root } = await mount()
    expect(root.querySelector('.overlay-title').textContent).toBe('Добавить роутер')
    expect(progress(root)).toBe('Шаг 1 из 4 · Как добавить')
    await click(button(root, 'Дальше'))
    expect(errorText(root)).toBe('Выберите, как добавить роутер.')
    await click(choice(root, 'Только выдать токен'))
    expect(errorText(root)).toBe('')
    expect(progress(root)).toBe('Шаг 1 из 3 · Как добавить')
    await click(button(root, 'Дальше'))
    expect(progress(root)).toBe('Шаг 2 из 3 · Роутер')
    cleanup(root)
  })

  it('шаг 2: имя по правилу сервера, подсказка правила видна', async () => {
    const { root } = await mount()
    await click(choice(root, 'Только выдать токен'))
    await click(button(root, 'Дальше'))
    expect(root.textContent).toContain('Имя роутера: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.')
    await fill(root, 'wizard-nickname', 'Дача')
    await click(button(root, 'Дальше'))
    expect(progress(root)).toBe('Шаг 2 из 3 · Роутер')
    expect(errorText(root)).toBe('Имя роутера: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.')
    await fill(root, 'wizard-nickname', 'dacha-1')
    await click(button(root, 'Дальше'))
    expect(progress(root)).toBe('Шаг 3 из 3 · Подтверждение')
    cleanup(root)
  })

  it('«Назад» -- на шаг назад; «Отмена» на первом шаге и «назад» слоя закрывают', async () => {
    const { root, calls } = await mount()
    await click(choice(root, 'Только выдать токен'))
    await click(button(root, 'Дальше'))
    await click(button(root, 'Назад'))
    expect(progress(root)).toBe('Шаг 1 из 3 · Как добавить')
    await click(button(root, 'Отмена'))
    expect(calls.closed).toBe(1)
    expect(root.querySelector('.overlay-back').textContent).toBe('Обслуживание')
    await click(root.querySelector('.overlay-back'))
    expect(calls.closed).toBe(2)
    cleanup(root)
  })

  it('токен: набор ника, тело запроса, токен один раз', async () => {
    mocks.reply = { nickname: 'car', raw_token: 'raw-tok-123', backend_url: 'https://wg.example.com', install_command: 'curl -fsSL https://wg.example.com/install.sh | sh' }
    const { root, calls } = await mount()
    await toConfirm(root, { path: 'Только выдать токен', nick: 'car', kind: 'В машине' })
    const summary = [...root.querySelectorAll('.wizard-summary .data-row')].map((r) => r.textContent)
    expect(summary).toEqual(['СпособТолько выдать токен', 'Имяcar', 'Где стоитВ машине'])
    const submit = button(root, 'Выдать токен')
    expect(submit.disabled).toBe(true)
    expect(root.querySelector('label[for="wizard-confirm-input"]').textContent).toBe('Наберите «car», чтобы подтвердить')
    await fill(root, 'wizard-confirm-input', ' CAR ')
    expect(submit.disabled).toBe(false)
    await click(submit)
    await flush()
    expect(mocks.bodies).toEqual([{ kind: 'register', nickname: 'car', agent_kind: 'mobile', confirm: ' CAR ' }])
    expect(calls.registered).toBe(1)
    expect(calls.started).toEqual([])
    expect(root.querySelector('.overlay-title').textContent).toBe('Токен для «car»')
    expect(root.querySelector('.token-value').textContent).toBe('raw-tok-123')
    expect(root.querySelector('.token-command').textContent).toBe('curl -fsSL https://wg.example.com/install.sh | sh')
    expect(root.textContent).toContain('Токен показывается один раз: закроете экран — увидеть его снова будет нельзя.')
    expect(root.textContent).toContain('https://wg.example.com')
    expect(button(root, 'Скопировать')).toBeTruthy()
    expect(button(root, 'Скопировать команду')).toBeTruthy()
    await click(button(root, 'Готово'))
    expect(calls.closed).toBe(1)
    cleanup(root)
  })

  it('установка: доступ, пароли скрыты и не уходят дальше экрана', async () => {
    mocks.reply = { job_id: 'j42', nickname: 'dacha-1' }
    const { root, calls } = await mount()
    await toConfirm(root, { path: 'Установить агента сейчас', kind: 'В машине' })
    expect(progress(root)).toBe('Шаг 3 из 4 · Доступ к роутеру')
    expect(root.textContent).toContain('Пароль уходит на сервер один раз и не сохраняется.')

    await click(button(root, 'Дальше'))
    expect(errorText(root)).toBe('Нужен адрес панели awg-manager: https://…')

    await fill(root, 'wizard-awgm-url', 'https://router.example.com')
    await fill(root, 'wizard-root-password', ROOT_PW)
    const rootInput = root.querySelector('#wizard-root-password')
    expect(rootInput.getAttribute('type')).toBe('password')
    expect(rootInput.getAttribute('autocomplete')).toBe('new-password')

    // Вход в веб по умолчанию: без логина и пароля панели дальше нельзя.
    expect(root.querySelector('#wizard-awgm-auth').value).toBe('web')
    await click(button(root, 'Дальше'))
    expect(errorText(root)).toBe('Для входа в веб нужны логин и пароль панели.')

    await fill(root, 'wizard-awgm-auth', 'api-key')
    expect(root.querySelector('#wizard-awgm-login')).toBe(null)
    expect(root.querySelector('#wizard-awgm-key').getAttribute('type')).toBe('password')
    await fill(root, 'wizard-awgm-key', KEY)
    await click(button(root, 'Дальше'))
    expect(progress(root)).toBe('Шаг 4 из 4 · Подтверждение')

    const summaryText = root.querySelector('.wizard-summary').textContent
    expect(summaryText).toContain('Ключ API')
    expect(summaryText).toContain('введён')
    expect(summaryText).not.toContain(ROOT_PW)
    expect(summaryText).not.toContain(KEY)

    await fill(root, 'wizard-confirm-input', 'dacha-1')
    await click(button(root, 'Установить'))
    await flush()
    expect(mocks.bodies).toEqual([
      {
        kind: 'provision', nickname: 'dacha-1', agent_kind: 'mobile', confirm: 'dacha-1',
        awgm_url: 'https://router.example.com', awgm_auth: 'api-key', root_password: ROOT_PW, version: '', awgm_api_key: KEY,
      },
    ])
    expect(calls.started).toEqual([{ jobId: 'j42', nickname: 'dacha-1' }])
    expect(JSON.stringify(calls.started)).not.toContain(ROOT_PW)
    expect(root.innerHTML).not.toContain(ROOT_PW)
    cleanup(root)
  })

  it('отказ по полю доступа -- обратно на шаг доступа, пароль стёрт', async () => {
    mocks.reply = new ApiError(400, 'root_password_required', 'x', 'Нужен пароль root')
    const { root } = await mount()
    await toConfirm(root, { path: 'Установить агента сейчас' })
    await fill(root, 'wizard-awgm-url', 'https://router.example.com')
    await fill(root, 'wizard-root-password', ROOT_PW)
    await fill(root, 'wizard-awgm-auth', 'none')
    await click(button(root, 'Дальше'))
    await fill(root, 'wizard-confirm-input', 'dacha-1')
    await click(button(root, 'Установить'))
    await flush()
    expect(progress(root)).toBe('Шаг 3 из 4 · Доступ к роутеру')
    expect(errorText(root)).toBe('Нужен пароль root')
    expect(root.querySelector('#wizard-root-password').value).toBe('')
    expect(root.querySelector('.wizard-secrets-cleared').textContent).toBe('Пароли стёрты после отправки — введите заново.')
    expect(root.querySelector('#wizard-awgm-url').value).toBe('https://router.example.com')
    cleanup(root)
  })

  it('отказ без своего шага -- остаёмся на подтверждении, набор сброшен', async () => {
    mocks.reply = new ApiError(503, 'provision_not_configured', 'x', 'Установка агентов на сервере не настроена')
    const { root } = await mount()
    await toConfirm(root, { path: 'Только выдать токен' })
    await fill(root, 'wizard-confirm-input', 'dacha-1')
    await click(button(root, 'Выдать токен'))
    await flush()
    expect(progress(root)).toBe('Шаг 3 из 3 · Подтверждение')
    expect(errorText(root)).toBe('Установка агентов на сервере не настроена')
    expect(root.querySelector('#wizard-confirm-input').value).toBe('')
    expect(button(root, 'Выдать токен').disabled).toBe(true)
    cleanup(root)
  })
})

describe('мастер во время отправки', () => {
  async function toSubmit(root, path) {
    await toConfirm(root, { path })
    if (path === 'Установить агента сейчас') {
      await fill(root, 'wizard-awgm-url', 'https://router.example.com')
      await fill(root, 'wizard-root-password', ROOT_PW)
      await fill(root, 'wizard-awgm-auth', 'none')
      await click(button(root, 'Дальше'))
    }
    await fill(root, 'wizard-confirm-input', 'dacha-1')
  }

  it('«назад» слоя и «Назад» мастера не закрывают; слой закреплён; job_id доходит', async () => {
    const d = deferred()
    mocks.pending = d.promise
    const { root, calls } = await mount()
    await toSubmit(root, 'Установить агента сейчас')
    await click(button(root, 'Установить'))
    expect(calls.busy).toEqual([true])
    await click(root.querySelector('.overlay-back'))
    expect(root.querySelector('.overlay-back').disabled).toBe(true)
    expect(button(root, 'Назад').disabled).toBe(true)
    expect(calls.closed).toBe(0)
    await act(async () => d.resolve({ job_id: 'j77', nickname: 'dacha-1' }))
    await flush()
    expect(calls.started).toEqual([{ jobId: 'j77', nickname: 'dacha-1' }])
    cleanup(root)
  })

  it('экран размонтирован до ответа -- «Ход работы» всё равно открывается', async () => {
    const d = deferred()
    mocks.pending = d.promise
    const { root, calls } = await mount()
    await toSubmit(root, 'Установить агента сейчас')
    await click(button(root, 'Установить'))
    cleanup(root)
    await act(async () => d.resolve({ job_id: 'j78', nickname: 'dacha-1' }))
    await flush()
    expect(calls.started).toEqual([{ jobId: 'j78', nickname: 'dacha-1' }])
  })

  it('токен: закрепление снимается, когда токен уже на экране', async () => {
    const d = deferred()
    mocks.pending = d.promise
    const { root, calls } = await mount()
    await toSubmit(root, 'Только выдать токен')
    await click(button(root, 'Выдать токен'))
    expect(calls.busy).toEqual([true])
    await act(async () => d.resolve({ nickname: 'dacha-1', raw_token: 'tok-1', backend_url: 'https://wg.example.com', install_command: 'sh' }))
    await flush()
    expect(root.querySelector('.token-value').textContent).toBe('tok-1')
    expect(calls.busy).toEqual([true, false])
    cleanup(root)
  })

  it('отказ -- закрепление снимается', async () => {
    mocks.reply = new ApiError(503, 'provision_not_configured', 'x')
    const { root, calls } = await mount()
    await toSubmit(root, 'Только выдать токен')
    await click(button(root, 'Выдать токен'))
    await flush()
    expect(calls.busy).toEqual([true, false])
    cleanup(root)
  })

  it('ответ без job_id -- слова, а не «Ход работы» пустого задания', async () => {
    mocks.reply = { nickname: 'dacha-1' }
    const { root, calls } = await mount()
    await toSubmit(root, 'Установить агента сейчас')
    await click(button(root, 'Установить'))
    await flush()
    expect(calls.started).toEqual([])
    expect(errorText(root)).toBe('Сервер не вернул номер задания — проверьте Парк: установка могла начаться.')
    expect(calls.busy).toEqual([true, false])
    cleanup(root)
  })
})
