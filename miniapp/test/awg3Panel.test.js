import { describe, it, expect } from 'vitest'
import {
  validatePanelForm,
  panelRequestBody,
  panelFormValues,
  panelRows,
  panelStateText,
  hhmm,
  awg3ErrorText,
  awg3FieldKey,
  checkView,
  errorBanner,
  handshakeText,
  trafficText,
  peerRows,
  summaryText,
  deviceNameProblem,
  routerPickRows,
  dmText,
  bytesToBase64,
  certHint,
  deletePanelSheetText,
  p12SizeProblem,
} from '../src/awg3Panel.js'

const at = (h, m) => new Date(2026, 8, 29, h, m).toISOString()

const NEW = { id: 'main', label: 'Main', base_url: 'https://panel.example.com', user: 'admin', password: 'pw', p12_base64: 'UDEy', p12_name: 'anex.p12', p12_password: 'x' }
const SAVED = { id: 'main', label: 'Main', base_url: 'https://panel.example.com', user: 'admin', password_set: true, cert_set: true, cert_subject: 'anex', state: 'ok' }

describe('форма панели', () => {
  it('новая: короткое имя, https, логин без «:», пароль и файл обязательны', () => {
    expect(validatePanelForm(NEW, { isNew: true })).toBe(null)
    expect(validatePanelForm({ ...NEW, id: 'Main!' }, { isNew: true }).field).toBe('id')
    expect(validatePanelForm({ ...NEW, base_url: 'http://panel.example.com' }, { isNew: true }).field).toBe('base_url')
    expect(validatePanelForm({ ...NEW, user: 'ad:min' }, { isNew: true }).field).toBe('user')
    expect(validatePanelForm({ ...NEW, password: '' }, { isNew: true }).field).toBe('password')
    expect(validatePanelForm({ ...NEW, p12_base64: '' }, { isNew: true }).field).toBe('p12')
  })

  it('правка: пароль не нужен, пока адрес и логин те же', () => {
    const v = { ...panelFormValues(SAVED), label: 'Main 2' }
    expect(validatePanelForm(v, { isNew: false, saved: SAVED })).toBe(null)
    const moved = validatePanelForm({ ...v, base_url: 'https://other.example.com' }, { isNew: false, saved: SAVED })
    expect(moved.field).toBe('password')
    expect(moved.text).toContain('введите пароль заново')
    expect(validatePanelForm({ ...v, base_url: 'https://panel.example.com/' }, { isNew: false, saved: SAVED })).toBe(null)
  })

  it('тело: id только у новой, пароль и .p12 -- только когда введены', () => {
    expect(panelRequestBody(NEW, { isNew: true })).toEqual({ id: 'main', label: 'Main', base_url: 'https://panel.example.com', user: 'admin', password: 'pw', p12_base64: 'UDEy', p12_password: 'x' })
    const edit = panelRequestBody({ ...panelFormValues(SAVED), label: ' Main 2 ' }, { isNew: false })
    expect(edit).toEqual({ label: 'Main 2', base_url: 'https://panel.example.com', user: 'admin' })
    expect('password' in edit || 'p12_base64' in edit || 'id' in edit).toBe(false)
  })

  it('значения формы никогда не несут секретов с сервера', () => {
    const v = panelFormValues({ ...SAVED, password: 'не должно быть' })
    expect(v.password).toBe('')
    expect(v.p12_base64).toBe('')
    expect(v.p12_password).toBe('')
  })

  it('подсказки сертификата и удаления', () => {
    expect(certHint(SAVED, panelFormValues(SAVED))).toContain('«anex»')
    expect(certHint(SAVED, { p12_name: 'new.p12' })).toContain('«new.p12»')
    expect(deletePanelSheetText(SAVED)).toMatchObject({ title: 'Удалить панель «Main»?', phrase: 'Main' })
  })
})

describe('список и состояния', () => {
  it('строки: хост и состояние словами', () => {
    const rows = panelRows([
      SAVED,
      { id: 'nl2', label: '', base_url: 'https://203.0.113.5:8444', state: 'paused', paused_until: at(14, 5) },
      { id: 'old', label: 'Old', base_url: 'https://panel.example.com', state: 'bad_password' },
      { id: 'ro', label: 'RO', base_url: 'https://ro.example.com', state: 'ok', readonly: true },
    ])
    expect(rows[0]).toMatchObject({ id: 'main', title: 'Main', sub: 'panel.example.com' })
    expect(rows[1]).toMatchObject({ title: 'nl2', sub: '203.0.113.5:8444 · вход ограничен до 14:05', tone: 'warn' })
    expect(rows[2].sub).toContain('неверный пароль')
    expect(rows[3].sub).toContain('только просмотр')
    expect(panelStateText({ state: 'cert_rejected' })).toBe('сертификат не принят')
    expect(hhmm(at(9, 7))).toBe('09:07')
    expect(hhmm('мусор')).toBe('')
  })

  it('отказы словами, пауза -- с ЧЧ:ММ', () => {
    expect(awg3ErrorText({ code: 'awg3_paused', data: { retry_at: at(14, 5) } })).toBe('Панель ограничила вход, повтор после 14:05.')
    expect(awg3ErrorText({ code: 'awg3_bad_password' })).toContain('пересохраните')
    expect(awg3ErrorText({ code: 'awg3_cert_rejected' })).toContain('.p12')
    expect(awg3ErrorText({ code: 'awg3_unreachable' })).toContain('10 секунд')
    expect(awg3ErrorText({ code: 'awg3_name_taken', serverMessage: 'Устройство с таким именем уже есть' })).toBe('Устройство с таким именем уже есть')
    expect(awg3ErrorText({ code: 'internal', serverMessage: 'boom' })).toBe('Не получилось. Попробуйте ещё раз.')
    expect(awg3FieldKey({ code: 'invalid_field', field: 'p12_password' })).toBe('p12_password')
    expect(awg3FieldKey({ code: 'invalid_field', field: 'router' })).toBe('')
  })

  it('итог проверки при сохранении', () => {
    expect(checkView(null)).toEqual({ tone: 'ok', text: 'Сохранено.' })
    expect(checkView({ ok: true, message: 'Панель ответила: интерфейсов — 2.' }).text).toContain('интерфейсов — 2')
    expect(checkView({ ok: false, code: 'awg3_paused', retry_at: at(10, 30) }).text).toContain('10:30')
    expect(checkView({ ok: false, code: 'awg3_bad_password', message: 'x' })).toMatchObject({ tone: 'bad' })
  })

  it('баннер экрана: что чинить и можно ли повторить', () => {
    expect(errorBanner({ code: 'awg3_bad_password' })).toMatchObject({ fix: true, retry: false })
    expect(errorBanner({ code: 'awg3_cert_rejected' })).toMatchObject({ fix: true, retry: false })
    expect(errorBanner({ code: 'awg3_paused', data: { retry_at: at(8, 0) } })).toMatchObject({ fix: false, retry: false, text: 'Панель ограничила вход, повтор после 08:00.' })
    expect(errorBanner({ code: 'awg3_unreachable' })).toMatchObject({ fix: false, retry: true })
  })

  // Решение задачи 9-10 (переопределяет спеку части 1): сертификат ПАНЕЛИ
  // (её собственный TLS, не наш .p12) отдельным состоянием и кодом.
  // cert_rejected с этого момента значит только «панель отвергла наш .p12».
  it('server_cert_rejected -- отдельно от cert_rejected нашего .p12', () => {
    expect(panelStateText({ state: 'server_cert_rejected' })).toBe('сертификат панели не прошёл проверку')
    expect(panelRows([{ id: 'x', label: 'X', base_url: 'https://x.example.com', state: 'server_cert_rejected' }])[0]).toMatchObject({ tone: 'warn' })
    expect(awg3ErrorText({ code: 'awg3_server_cert_rejected' })).toBe('Сертификат панели не прошёл проверку — проверьте адрес панели и сертификат на сервере.')
    expect(errorBanner({ code: 'awg3_server_cert_rejected' })).toMatchObject({ fix: true, retry: false })
  })
})

describe('пиры', () => {
  // Правка 1 (ревью раунд 1): value-колонка -- только короткое время, без
  // слова «handshake» (англицизм) и без «выключен» (тот текст переехал в
  // ярлык под именем пира, отдельно от времени).
  it('обмен ключами коротко: никогда, только что, минуты, часы, дни', () => {
    expect(handshakeText(-1)).toBe('не подключался')
    expect(handshakeText(0)).toBe('только что')
    expect(handshakeText(125)).toBe('2 мин назад')
    expect(handshakeText(3 * 3600 + 5)).toBe('3 ч назад')
    expect(handshakeText(50 * 3600)).toBe('2 дн назад')
  })

  it('строки: точка, трафик ↓/↑, ярлык роутера или «выключен»', () => {
    const rows = peerRows([
      { id: 'p1', name: 'wgmon-home', state: 'online', handshake_age_sec: 30, rx_bytes: 1536, tx_bytes: 2 * 1024 * 1024, router: { id: 7, nickname: 'home' } },
      { id: 'p2', name: 'laptop', state: 'never', handshake_age_sec: -1, rx_bytes: 0, tx_bytes: 0, router: null },
      { id: 'p3', name: 'tablet', state: 'off', handshake_age_sec: -1 },
    ])
    expect(rows[0]).toMatchObject({ dot: 'ok', value: 'только что', valueSub: '↓\u00a01,5\u202fКБ · ↑\u00a02,0\u202fМБ', router: { id: 7, nickname: 'home' }, off: false })
    expect(rows[1]).toMatchObject({ dot: 'muted', value: 'не подключался', valueSub: '', router: null, off: false })
    // off -- своя точка (muted, как у never) и свой ярлык под именем, а не
    // текст в колонке времени.
    expect(rows[2]).toMatchObject({ dot: 'muted', value: 'не подключался', router: null, off: true })
    expect(trafficText(0, 0)).toBe('')
    expect(summaryText({ peers_total: 6, peers_online: 4 })).toBe('онлайн 4 из 6')
    expect(summaryText({ peers_total: 0 })).toBe('устройств нет')
  })

  it('имя устройства -- правила панели и бота', () => {
    expect(deviceNameProblem('iphone-anex')).toBe('')
    expect(deviceNameProblem('  ')).toContain('Введите')
    expect(deviceNameProblem('я'.repeat(41))).toContain('40')
    expect(deviceNameProblem('a[b]')).toContain('«[»')
    expect(deviceNameProblem('WGMON-x')).toContain('роутерам')
  })

  it('выбор роутера: по алфавиту, с пометкой, у кого пир уже есть', () => {
    const rows = routerPickRows([{ id: 2, nickname: 'work' }, { id: 7, nickname: 'home' }, { id: 9 }], [{ router: { id: 7, nickname: 'home' } }])
    expect(rows.map((r) => r.title)).toEqual(['home', 'work'])
    expect(rows[0].sub).toBe('запись на панели уже есть — возьмём её конфиг')
    expect(rows[1].sub).toBe('на панели появится запись «wgmon-work»')
  })

  it('личка словами', () => {
    expect(dmText('sent')).toContain('в личку')
    expect(dmText('unreachable')).toContain('/start')
    expect(dmText('что-то новое')).toBe(dmText('failed'))
  })

  // Решение задачи 9-10: dm может быть «файл дошёл, QR — нет».
  it('личка: sent_no_qr отличается от sent', () => {
    expect(dmText('sent_no_qr')).toContain('QR не отправился')
    expect(dmText('sent_no_qr')).not.toBe(dmText('sent'))
  })
})

describe('файл .p12', () => {
  it('bytesToBase64 -- как btoa, в том числе на больших файлах', () => {
    expect(bytesToBase64(new Uint8Array([0x50, 0x31, 0x32]))).toBe('UDEy')
    const big = new Uint8Array(70_000).map((_, i) => i % 256)
    expect(atob(bytesToBase64(big)).length).toBe(70_000)
  })

  // Правка 5 (ревью раунд 1, сужено раундом 3 финального ревью): клиентский
  // потолок был 100 КБ при бэкендовом 64 КБ (p12.go maxP12Size) -- файл
  // между 64 и 100 КБ проходил браузер и падал только на сервере. Потолки
  // сведены к одному числу — 64 КБ.
  it('p12SizeProblem -- потолок 64 КБ словами', () => {
    expect(p12SizeProblem(1024)).toBe('')
    expect(p12SizeProblem(64 * 1024)).toBe('')
    expect(p12SizeProblem(64 * 1024 + 1)).toBe('Файл .p12 больше 64 КБ — это не похоже на сертификат.')
  })
})
