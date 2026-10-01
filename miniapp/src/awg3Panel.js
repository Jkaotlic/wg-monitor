import { formatBytes } from './traffic.js'
import { agoText } from './when.js'

// awg3-панели оператора (v0.49): форма, строки списка, состояния, пиры и
// тексты. Только админ. Пароль панели и .p12 живут только в значениях формы
// до отправки; сервер их не возвращает -- вместо них password_set и cert_set.

export const PANEL_ID_RE = /^[a-z][a-z0-9_-]{1,15}$/

export const AWG3_TEXTS = {
  group: 'Панели VPN-серверов',
  groupIntro: 'Панели выпуска конфигов на ваших VPS: устройства, «Конфиг на устройство» и «Выпустить на роутер».',
  empty: 'Панелей пока нет.',
  add: 'Добавить панель',
  loading: 'Читаем панели…',
  loadError: 'Не удалось прочитать список панелей.',
  notConfigured: 'Панели awg3 на этом сервере не настроены.',
  notFound: 'Такой панели больше нет — вернитесь к списку.',
  newTitle: 'Новая панель',
  saveNew: 'Сохранить и проверить',
  saveEdit: 'Сохранить',
  saving: 'Сохраняем…',
  saveHint: 'Проверка — один запрос к панели. Пароль не подбирается: после отказа бот к панели не обращается, пока вы не пересохраните учётные данные.',
  certSection: 'Клиентский сертификат',
  p12Label: 'Файл .p12',
  p12Pick: 'Выбрать файл .p12',
  p12Password: 'Пароль от .p12',
  p12PasswordHint: 'Нужен, только чтобы достать сертификат. Сам файл и этот пароль не хранятся.',
  p12ReadError: 'Файл не прочитался — выберите его ещё раз.',
  deleteButton: 'Удалить панель',
  settings: 'Настройки панели',
  readonly: 'Панель только для просмотра: выпускать с неё нельзя.',
  peersLoading: 'Спрашиваем панель…',
  peersHint: 'Время — с последнего обмена ключами; ниже — трафик за интерфейс.',
  noPeers: 'На этом интерфейсе устройств нет.',
  noIfaces: 'Панель не назвала ни одного интерфейса.',
  retry: 'Повторить',
  device: 'Конфиг на устройство',
  deviceHint: 'На панели появится новое устройство. QR покажется здесь, а файл .conf и QR придут вам в личку.',
  deviceName: 'Имя устройства',
  devicePlaceholder: 'iphone-anex',
  deviceIssue: 'Выпустить',
  deviceBusy: 'Выпускаем…',
  qrAlt: 'QR-код конфига',
  qrNote: 'В QR и в файле приватный ключ — не пересылайте их.',
  router: 'Выпустить на роутер',
  routerHint: 'Если на панели уже есть запись «wgmon-<роутер>», бот возьмёт её конфиг заново — новой записи не будет. Конфиг встанет на роутер VPN-туннелем.',
  routerPick: 'Какому роутеру',
  routerNone: 'В парке нет роутеров.',
  routerWaiting: 'Конфиг выпущен, ждём подтверждения роутера…',
}

export const PANEL_FIELDS = [
  { key: 'label', label: 'Название', placeholder: 'Main' },
  { key: 'id', label: 'Короткое имя', placeholder: 'main', newOnly: true, hint: 'Латиница, цифры, «-» и «_». Потом не меняется.' },
  { key: 'base_url', label: 'Адрес панели', placeholder: 'https://panel.example.com', inputMode: 'url' },
  { key: 'user', label: 'Логин', placeholder: 'admin' },
  { key: 'password', label: 'Пароль панели', kind: 'password' },
]

const trim = (v) => (typeof v === 'string' ? v.trim() : '')

export function panelFormValues(panel) {
  return {
    id: panel?.id ?? '',
    label: panel?.label ?? '',
    base_url: panel?.base_url ?? '',
    user: panel?.user ?? '',
    password: '',
    p12_base64: '',
    p12_name: '',
    p12_password: '',
  }
}

// Проблема формы до сервера: { field, text } или null. Правила -- как у
// awg3panel.validateInstance; сервер проверит ещё раз.
export function validatePanelForm(values, { isNew, saved = null } = {}) {
  if (isNew && !PANEL_ID_RE.test(trim(values?.id))) {
    return { field: 'id', text: 'Короткое имя: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.' }
  }
  const base = trim(values?.base_url).replace(/\/+$/, '')
  if (!/^https:\/\/[^\s/?#]+/i.test(base)) return { field: 'base_url', text: 'Адрес панели — https://имя-или-IP[:порт].' }
  const user = trim(values?.user)
  if (!user || user.includes(':')) return { field: 'user', text: 'Логин панели — не пустой и без «:».' }
  const moved = !isNew && saved != null && (base !== saved.base_url || user !== saved.user)
  if ((isNew || moved) && !values?.password) {
    return { field: 'password', text: moved ? 'Адрес или логин изменены — введите пароль заново.' : 'Укажите пароль панели.' }
  }
  if (isNew && !values?.p12_base64) return { field: 'p12', text: 'Выберите файл .p12 с клиентским сертификатом.' }
  return null
}

// Пароль не обрезается: пробел может быть его частью. Пустой -- ключа нет,
// сервер оставляет прежний; .p12 -- только выбранный заново.
export function panelRequestBody(values, { isNew }) {
  const body = {}
  if (isNew) body.id = trim(values?.id)
  body.label = trim(values?.label)
  body.base_url = trim(values?.base_url)
  body.user = trim(values?.user)
  if (values?.password) body.password = values.password
  if (values?.p12_base64) {
    body.p12_base64 = values.p12_base64
    body.p12_password = values.p12_password ?? ''
  }
  return body
}

export function hhmm(iso) {
  const d = new Date(iso)
  if (!iso || Number.isNaN(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function hostOf(url) {
  try {
    return new URL(url).host
  } catch {
    return String(url ?? '')
  }
}

// server_cert_rejected -- решение задачи 9-10 (переопределяет спеку части
// 1): сертификат САМОЙ ПАНЕЛИ (её TLS) не прошёл проверку; отдельно от
// cert_rejected, которое с этого момента значит только «панель отвергла наш
// .p12».
export function panelStateText(panel) {
  switch (panel?.state) {
    case 'disabled':
      return 'выключена'
    case 'bad_password':
      return 'неверный пароль — пересохраните'
    case 'cert_rejected':
      return 'сертификат не принят'
    case 'server_cert_rejected':
      return 'сертификат панели не прошёл проверку'
    case 'paused':
      return `вход ограничен до ${hhmm(panel.paused_until)}`
    default:
      return panel?.readonly ? 'только просмотр' : ''
  }
}

const WARN_STATES = ['bad_password', 'cert_rejected', 'server_cert_rejected', 'paused']

export function panelRows(panels) {
  return (Array.isArray(panels) ? panels : []).map((p) => ({
    id: String(p.id),
    title: p.label || String(p.id),
    sub: [hostOf(p.base_url), panelStateText(p)].filter(Boolean).join(' · '),
    tone: WARN_STATES.includes(p.state) ? 'warn' : undefined,
  }))
}

const ERROR_TEXTS = {
  awg3_bad_password: 'Неверный пароль — пересохраните учётные данные панели. До этого бот к ней не обращается.',
  awg3_cert_rejected: 'Сертификат не принят — загрузите файл .p12 заново.',
  awg3_server_cert_rejected: 'Сертификат панели не прошёл проверку — проверьте адрес панели и сертификат на сервере.',
  awg3_unreachable: 'Панель недоступна — нет связи или ответа за 10 секунд.',
  awg3_bad_response: 'Панель ответила не так, как ожидалось — проверьте адрес.',
  awg3_readonly: 'Панель только для просмотра: выпускать с неё нельзя.',
  awg3_disabled: 'Панель выключена.',
  awg3_not_found: 'Такой панели больше нет — вернитесь к списку.',
  awg3_not_configured: 'Панели awg3 на этом сервере не настроены.',
  confirm_mismatch: 'Название панели набрано не так.',
}

export function pausedText(retryAt) {
  const t = hhmm(retryAt)
  return t ? `Панель ограничила вход, повтор после ${t}.` : 'Панель ограничила вход — повторите позже.'
}

export function awg3ErrorText(err) {
  const code = err?.code
  if (code === 'awg3_paused') return pausedText(err?.data?.retry_at)
  if (ERROR_TEXTS[code]) return ERROR_TEXTS[code]
  if (err?.serverMessage && typeof code === 'string' && (code.startsWith('awg3_') || code === 'invalid_field' || code === 'missing_iface')) {
    return err.serverMessage
  }
  return 'Не получилось. Попробуйте ещё раз.'
}

const FORM_KEYS = ['id', 'label', 'base_url', 'user', 'password', 'p12', 'p12_password']

// Поле формы, которое отверг сервер (invalid_field); незнакомое -- пусто.
export function awg3FieldKey(err) {
  if (err?.code !== 'invalid_field') return ''
  return FORM_KEYS.includes(err.field) ? err.field : ''
}

export function checkView(check) {
  if (!check) return { tone: 'ok', text: 'Сохранено.' }
  if (check.ok) return { tone: 'ok', text: check.message || 'Панель ответила.' }
  if (check.code === 'awg3_paused') return { tone: 'bad', text: pausedText(check.retry_at) }
  return { tone: 'bad', text: ERROR_TEXTS[check.code] || check.message || 'Проверка не прошла.' }
}

// Баннер экрана панели по отказу: fix -- чинится в настройках панели,
// retry -- имеет смысл повторить сразу.
export function errorBanner(err) {
  const code = err?.code
  return {
    text: awg3ErrorText(err),
    fix: code === 'awg3_bad_password' || code === 'awg3_cert_rejected' || code === 'awg3_server_cert_rejected' || code === 'awg3_disabled',
    retry: code === 'awg3_unreachable' || code === 'awg3_bad_response' || code === 'unknown',
  }
}

// handshake_age_sec -1 -- ни одного handshake: «не подключался», а не
// «55 лет назад».
// Правка 1-2 (ревью раунд 1): колонка времени -- ТОЛЬКО короткая форма, без
// англицизма «handshake» и без «выключен» (тот текст переехал в ярлык под
// именем пира, peerRows ниже). Состояние пира сюда больше не приходит --
// «никогда» уже целиком читается по возрасту (-1 или не число).
export function handshakeText(ageSec) {
  if (typeof ageSec !== 'number' || ageSec < 0) return 'не подключался'
  return agoText(ageSec)
}

// ↓ -- принято сервером, ↑ -- отдано, как в консоли панели.
export function trafficText(rx, tx) {
  if (!rx && !tx) return ''
  // Неразрывные пробелы внутри каждой половины: строка может перенестись
  // только на « · », а «↑ 200,3 МБ» не рвётся и не вылезает за карточку.
  const nb = (t) => t.replace(/ /g, '\u00a0')
  return `${nb(`↓ ${formatBytes(rx ?? 0)}`)} · ${nb(`↑ ${formatBytes(tx ?? 0)}`)}`
}

export const PEER_DOT = { online: 'ok', idle: 'warn', never: 'muted', off: 'muted' }

// Правка 1 (ревью раунд 1): один .data-row на пира (без обёртки
// .awg3-peer). router -- ярлык «роутер «nick»» под именем; off -- пир
// выключен и без ярлыка роутера, тогда под именем -- «выключен». Оба ярлыка
// взаимоисключающие: выключенный пир с уже известным роутером всё равно
// подписан роутером -- это важнее, чем факт паузы.
export function peerRows(peers) {
  return (Array.isArray(peers) ? peers : []).map((p) => {
    const router = p.router && p.router.nickname ? { id: p.router.id, nickname: String(p.router.nickname) } : null
    return {
      id: String(p.id),
      title: p.name || String(p.id),
      state: p.state,
      dot: PEER_DOT[p.state],
      value: handshakeText(p.handshake_age_sec),
      valueSub: trafficText(p.rx_bytes, p.tx_bytes),
      router,
      off: !router && p.state === 'off',
    }
  })
}

export function summaryText(summary) {
  const total = summary?.peers_total ?? 0
  if (!total) return 'устройств нет'
  return `онлайн ${summary.peers_online ?? 0} из ${total}`
}

// Правила имени -- как у панели (1..40 знаков, без [ ] и переводов строки) и
// у бота (префикс wgmon- -- роутерам).
export function deviceNameProblem(name) {
  const n = trim(name)
  if (!n) return 'Введите имя устройства, например iphone-anex.'
  if ([...n].length > 40) return 'Имя устройства — до 40 знаков.'
  if (/[[\]\r\n]/.test(n)) return 'Имя устройства — без «[», «]» и переводов строки.'
  if (n.toLowerCase().startsWith('wgmon-')) return 'Имена «wgmon-…» бот оставляет роутерам — выберите другое.'
  return ''
}

export function routerPickRows(routers, peers) {
  const taken = new Set((Array.isArray(peers) ? peers : []).map((p) => p?.router?.id).filter((v) => v != null))
  return (Array.isArray(routers) ? routers : [])
    .filter((r) => r && r.nickname)
    .map((r) => ({
      id: r.id,
      title: String(r.nickname),
      sub: taken.has(r.id) ? 'запись на панели уже есть — возьмём её конфиг' : `на панели появится запись «wgmon-${r.nickname}»`,
    }))
    .sort((a, b) => a.title.localeCompare(b.title))
}

// sent_no_qr (решение задачи 9-10) -- .conf дошёл в личку, а QR-фото нет:
// QR всё равно рабочий, он уже на экране.
const DM_TEXTS = {
  sent: 'Файл .conf и QR отправлены вам в личку.',
  sent_no_qr: 'Файл в личке, QR не отправился — он на экране.',
  unreachable: 'В личку не отправилось: откройте бота и нажмите /start. QR выше — рабочий.',
  failed: 'Telegram не принял файл. QR выше — рабочий.',
  not_configured: 'Отправка в личку на сервере не настроена. QR выше — рабочий.',
}

export function dmText(dm) {
  return DM_TEXTS[dm] ?? DM_TEXTS.failed
}

// Правка 5 (ревью раунд 1, сужено раундом 3 финального ревью): потолок на
// стороне браузера был отдельным числом (100 КБ) от бэкендового
// (awg3panel/p12.go maxP12Size, 64 КБ) -- файл между ними проходил браузер и
// падал только на сервере. Оба потолка сведены к одному числу.
const P12_MAX_BYTES = 64 * 1024

export function p12SizeProblem(size) {
  return typeof size === 'number' && size > P12_MAX_BYTES ? 'Файл .p12 больше 64 КБ — это не похоже на сертификат.' : ''
}

export function bytesToBase64(bytes) {
  let bin = ''
  const CHUNK = 0x8000
  for (let i = 0; i < bytes.length; i += CHUNK) bin += String.fromCharCode(...bytes.subarray(i, i + CHUNK))
  return btoa(bin)
}

export async function readFileBase64(file) {
  const buf = await file.arrayBuffer()
  return bytesToBase64(new Uint8Array(buf))
}

export function certHint(panel, values) {
  if (values?.p12_name) return `Выбран файл «${values.p12_name}».`
  if (panel?.cert_set) {
    const until = panel.cert_not_after ? ` до ${new Date(panel.cert_not_after).toLocaleDateString('ru-RU')}` : ''
    return `Сертификат «${panel.cert_subject || 'без имени'}»${until}. Новый файл заменит его.`
  }
  return 'Тот же .p12, которым браузер входит в панель. Файл и его пароль не хранятся — только сертификат из него.'
}

export function passwordHint(panel, { isNew }) {
  if (isNew) return 'Пароль хранится на сервере бота и наружу не отдаётся.'
  return panel?.password_set ? 'Пароль задан. Пустое поле оставит его как есть.' : 'Пароль не задан.'
}

export function deletePanelSheetText(panel) {
  const name = panel?.label || panel?.id || ''
  return {
    title: `Удалить панель «${name}»?`,
    body: 'Панель пропадёт из бота. На самой панели и на роутерах ничего не меняется: выпущенные устройства и VPN-туннели остаются.',
    phrase: name,
  }
}
