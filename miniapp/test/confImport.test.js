import { describe, it, expect } from 'vitest'
import {
  CONF_MAX_BYTES,
  TUNNEL_NAME_RE,
  confFileProblem,
  suggestTunnelName,
  tunnelNameProblem,
  bytesToBase64,
  readConfBase64,
  previewView,
  importErrorText,
  importOutcome,
  withPickAgain,
  IMPORT_POLL_DEADLINE_MS,
  IMPORT_TEXTS,
} from '../src/confImport.js'
import { ApiError } from '../src/api.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'

const RULE = 'Имя не подходит: латиница в нижнем регистре, цифры, «-» и «_»; от 2 до 32 знаков, первая — буква.'

describe('файл', () => {
  it('расширение .conf и не больше 50 КиБ -- до чтения', () => {
    expect(CONF_MAX_BYTES).toBe(50 * 1024)
    expect(confFileProblem({ name: 'home.conf', size: 300 })).toBe('')
    expect(confFileProblem({ name: 'HOME.CONF', size: 300 })).toBe('')
    expect(confFileProblem({ name: 'home.conf', size: CONF_MAX_BYTES })).toBe('')
    expect(confFileProblem({ name: 'home.txt', size: 300 })).toBe('Нужен файл с расширением .conf — конфиг WireGuard или AmneziaWG.')
    expect(confFileProblem({ name: 'home.conf', size: 0 })).toBe('Файл пустой.')
    expect(confFileProblem({ name: 'home.conf', size: CONF_MAX_BYTES + 1 })).toBe('Файл больше 50 КиБ — это не конфиг VPN-туннеля.')
    expect(confFileProblem(null)).toBe('Файл не выбран.')
  })

  it('base64 -- побайтно, и через границу куска', () => {
    expect(bytesToBase64(new TextEncoder().encode('[Interface]\n'))).toBe(btoa('[Interface]\n'))
    expect(bytesToBase64(new TextEncoder().encode('a'.repeat(70_000)))).toBe(btoa('a'.repeat(70_000)))
    expect(bytesToBase64(new Uint8Array(0))).toBe('')
  })

  it('чтение файла FileReader-ом', async () => {
    class FakeReader {
      readAsArrayBuffer(file) {
        this.result = Uint8Array.from(new TextEncoder().encode(file.text)).buffer
        queueMicrotask(() => this.onload())
      }
    }
    class BrokenReader {
      readAsArrayBuffer() {
        queueMicrotask(() => this.onerror())
      }
    }
    expect(await readConfBase64({ text: '[Peer]\n' }, FakeReader)).toBe(btoa('[Peer]\n'))
    await expect(readConfBase64({ text: '' }, BrokenReader)).rejects.toThrow('read_failed')
  })
})

describe('имя VPN-туннеля', () => {
  it('подсказка из имени файла -- по правилу роутера', () => {
    expect(suggestTunnelName('Amsterdam NL.conf')).toBe('amsterdam-nl')
    expect(suggestTunnelName('wg_home.CONF')).toBe('wg_home')
    expect(suggestTunnelName('1.conf')).toBe('vpn-1')
    expect(suggestTunnelName('Москва.conf')).toBe('')
    expect(suggestTunnelName(`${'a'.repeat(40)}.conf`)).toBe('a'.repeat(32))
    expect(TUNNEL_NAME_RE.test(suggestTunnelName('--x--y--.conf'))).toBe(true)
  })

  it('проверка имени и занятые имена', () => {
    const snap = { tunnels: [{ id: 'nwg1', name: 'Amsterdam' }] }
    expect(tunnelNameProblem('')).toBe('Введите имя VPN-туннеля.')
    expect(tunnelNameProblem('Amsterdam2')).toBe(RULE)
    expect(tunnelNameProblem('a')).toBe(RULE)
    expect(tunnelNameProblem('9lives')).toBe(RULE)
    expect(tunnelNameProblem('  spare ')).toBe('')
    expect(tunnelNameProblem('amsterdam', snap)).toBe('VPN-туннель «amsterdam» уже есть на роутере — выберите другое имя.')
    expect(tunnelNameProblem('nwg1', snap)).toBe('VPN-туннель «nwg1» уже есть на роутере — выберите другое имя.')
  })
})

// Форма ответа -- контракт части 1: {token, name, state, analyzed, note?,
// can_confirm, preview:{endpoint, addresses[], dns[], mtu?, problems[{severity, code?, message}]}}.
describe('предпросмотр', () => {
  it('поля и замечания', () => {
    const v = previewView({
      token: 't1',
      state: 'ready',
      analyzed: true,
      can_confirm: false,
      preview: {
        endpoint: '203.0.113.7:51820',
        addresses: ['198.51.100.2/32', ' '],
        dns: '198.51.100.53, 198.51.100.54',
        mtu: 1280,
        problems: ['Нет PersistentKeepalive', { code: 'mtu_low', message: 'MTU ниже рекомендуемого', severity: 'warning' }, { code: 'bad_key', severity: 'ERROR' }, {}, null],
      },
    })
    expect(v).toEqual({
      endpoint: '203.0.113.7:51820',
      addresses: '198.51.100.2/32',
      dns: '198.51.100.53, 198.51.100.54',
      mtu: '1280',
      problems: [
        { tone: 'warn', text: 'Нет PersistentKeepalive' },
        { tone: 'warn', text: 'MTU ниже рекомендуемого' },
        { tone: 'error', text: 'bad_key' },
      ],
      analyzed: true,
      analyzing: false,
      note: '',
      blocking: true,
      canConfirm: false,
    })
  })

  it('кнопку решает can_confirm сервера', () => {
    expect(previewView({ state: 'ready', analyzed: true, can_confirm: true, preview: {} }).canConfirm).toBe(true)
    // Сервер ещё анализирует -- нажимать нельзя, даже если поле потерялось.
    expect(previewView({ state: 'analyzing', analyzed: false, preview: {} })).toMatchObject({ analyzing: true, canConfirm: false })
    expect(previewView({ state: 'ready', analyzed: true, preview: { problems: [{ severity: 'error', message: 'x' }] } }).canConfirm).toBe(false)
    expect(previewView({ state: 'ready', analyzed: true, preview: {} }).canConfirm).toBe(true)
  })

  it('пустой ответ и проверка пропущена -- слова вместо пустоты', () => {
    expect(previewView({ state: 'ready', analyzed: false, can_confirm: true, note: 'Агент старше v0.28 — проверка пропущена.' })).toEqual({
      endpoint: 'не указан',
      addresses: 'не указаны',
      dns: 'не указаны',
      mtu: 'по умолчанию',
      problems: [],
      analyzed: false,
      analyzing: false,
      note: 'Агент старше v0.28 — проверка пропущена.',
      blocking: false,
      canConfirm: true,
    })
    expect(previewView(null).analyzed).toBe(false)
    expect(previewView(null).canConfirm).toBe(false)
  })
})

describe('ошибки и итог', () => {
  it('фраза сервера, иначе своя по коду', () => {
    expect(importErrorText(new ApiError(400, 'invalid_conf', 'x', 'В файле нет секции [Peer]'))).toBe('В файле нет секции [Peer]')
    expect(importErrorText(new ApiError(400, 'invalid_conf', 'x'))).toBe('Это не похоже на конфиг WireGuard или AmneziaWG — проверьте файл и выберите его заново.')
    expect(importErrorText(new ApiError(400, 'invalid_name', 'x'))).toBe(RULE)
    expect(importErrorText(new ApiError(409, 'name_taken', 'x'))).toBe('VPN-туннель с таким именем уже есть на роутере — выберите другое имя.')
    expect(importErrorText(new ApiError(400, 'conf_too_large', 'x'))).toBe('Файл больше 50 КиБ — это не конфиг VPN-туннеля.')
    expect(importErrorText(new ApiError(410, 'preview_expired', 'x'))).toBe('Проверка устарела: с неё прошло больше 5 минут. Выберите файл заново.')
    expect(importErrorText(new ApiError(409, 'preview_not_ready', 'x'))).toBe('Роутер ещё проверяет конфиг — подождите немного.')
    expect(importErrorText(new ApiError(409, 'conf_rejected', 'x'))).toBe('Роутер не примет этот конфиг — исправьте ошибки в файле и выберите его заново.')
    expect(importErrorText(new ApiError(409, 'agent_too_old', 'x'))).toBe('Загружать конфиги из приложения этот агент не умеет — обновите агента на роутере.')
    expect(importErrorText(new Error('net'))).toBe('Сервер не ответил — попробуйте ещё раз.')
    expect(importErrorText(new ApiError(500, 'unknown', 'x'))).toBe('Не получилось. Попробуйте ещё раз.')
  })

  it('итог команды', () => {
    expect(importOutcome({ status: 'ok', output: '' }, 'amsterdam')).toEqual({
      tone: 'ok',
      text: 'VPN-туннель «amsterdam» добавлен. Перенести на него правила можно в «Маршрутах».',
      done: true,
    })
    expect(importOutcome({ status: 'err', output: 'awg-manager: bad config' }, 'amsterdam').text).toBe('Роутер не добавил VPN-туннель: awg-manager: bad config')
    expect(importOutcome({ status: 'err', output: 'unknown action: tunnel_import' }, 'amsterdam').text).toBe(AGENT_OLDER_THAN_APP)
    expect(importOutcome(null, 'amsterdam')).toEqual({
      tone: 'warn',
      text: 'Команда ушла на роутер, но он пока не ответил. Загляните в список VPN-туннелей через минуту.',
      done: false,
    })
  })

  it('экран говорит про замену словами и про приватный ключ', () => {
    expect(IMPORT_TEXTS.replaceHint).toContain('добавьте новый')
    expect(IMPORT_TEXTS.replaceHint).toContain('перенесите на него правила в «Маршрутах»')
    expect(IMPORT_TEXTS.replaceHint).toContain('удалите прежний')
    expect(IMPORT_TEXTS.replaceHint).toContain('«Заменить конфиг VPN-туннеля»')
    expect(IMPORT_TEXTS.privacy).toContain('приватный ключ')
    expect(IMPORT_TEXTS.notAnalyzed).toContain('v0.28')
    for (const t of Object.values(IMPORT_TEXTS)) expect(t).not.toMatch(/(^|[^-])туннел/i)
  })
})

// Ревью цикла 4.
describe('ревью: опрос и повторный выбор файла', () => {
  it('опрос предпросмотра кончается раньше, чем живёт токен (5 минут)', () => {
    expect(IMPORT_POLL_DEADLINE_MS).toBe(4 * 60_000)
  })

  it('analysis_pending -- проверка ещё идёт', () => {
    expect(importErrorText(new ApiError(409, 'analysis_pending', 'x'))).toBe('Проверка ещё идёт — роутер пока не закончил проверять конфиг.')
    expect(IMPORT_TEXTS.analyzing).toMatch(/^Проверка ещё идёт/)
    expect(IMPORT_TEXTS.analyzeSlow).toMatch(/^Проверка ещё идёт/)
  })

  it('после отказа проверки -- выбрать файл заново, без повтора слов', () => {
    expect(withPickAgain('Сервер не ответил — попробуйте ещё раз.')).toBe('Сервер не ответил — попробуйте ещё раз. Выберите файл заново.')
    expect(withPickAgain('Проверка устарела: с неё прошло больше 5 минут. Выберите файл заново.')).toBe('Проверка устарела: с неё прошло больше 5 минут. Выберите файл заново.')
    expect(withPickAgain('')).toBe('Выберите файл заново.')
  })
})
