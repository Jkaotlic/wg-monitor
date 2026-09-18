// Экран настроек: по каким правилам бот судит об этом роутере и что на нём
// стоит из версий. Чистые функции отдельно от экрана -- их проверяет тест, а
// вёрстку нет.
//
// Все числа здесь ЖИВЫЕ: пороги приезжают из backend.yaml через
// /routers/{id}/settings, версии -- из version_audit. Нарисованная настройка,
// которой нет, хуже отсутствующей строки: по ней человек считает, через
// сколько придёт тревога.

import { humanAge, pluralRu } from './labels.js'

function checksCount(n) {
  return `${n} ${pluralRu(n, 'проверка', 'проверки', 'проверок')} подряд`
}

// Пороги, по которым бот судит о роутере. Путь в конфиге («state.fail_threshold»)
// здесь не показывается: файл живёт на сервере, где запущен бот, и владелец
// роутера не может ни найти его, ни изменить. Экран говорит об этом словами,
// а ключ рядом с числом остаётся шумом.
//
// Имена программ на самом роутере -- другое дело: «awg-manager» и «HydraRoute Neo»
// человек видит в его собственной панели, и подпись помогает их узнать
// (auditRows ниже).
export function thresholdRows(settings) {
  if (!settings) return []
  const rows = []
  if (settings.silence_after_sec) {
    rows.push({
      key: 'silence',
      title: 'Считаем, что роутер молчит, после',
      value: `${humanAge(settings.silence_after_sec)} без отчёта`,
    })
  }
  // Только у мобильного: у статичного «молчит» и «выключен» -- одно событие,
  // и вторая строка была бы копией первой другими словами.
  if (settings.mobile && settings.offline_after_sec) {
    rows.push({
      key: 'offline',
      title: 'Считаем выключенным после',
      value: `${humanAge(settings.offline_after_sec)} молчания`,
    })
  }
  if (settings.alert_after_fails) {
    rows.push({
      key: 'alert',
      title: 'Тревога, если проверка провалилась',
      value: checksCount(settings.alert_after_fails),
    })
  }
  if (settings.recovery_after_oks) {
    rows.push({
      key: 'recovery',
      title: 'Отбой, если снова в порядке',
      value: checksCount(settings.recovery_after_oks),
    })
  }
  return rows
}

// Строка «Агент на роутере» -- в разделе «Что стоит на роутере» (v0.41,
// спека C3): версия агента -- то, что стоит на роутере, а не порог тревоги.
export function agentRow(settings) {
  if (!settings?.agent_version) return null
  return { key: 'agent', title: 'Агент на роутере', value: settings.agent_version }
}

// version_audit отвечает JSON'ом (wire.VersionAudit): версии, а не текст.
export function auditRows(output) {
  let audit = null
  try {
    audit = JSON.parse(output)
  } catch {
    return []
  }
  if (!audit || typeof audit !== 'object') return []
  const rows = []
  if (audit.awgmgr_version) {
    rows.push({
      key: 'awgmgr',
      title: 'Панель роутера',
      code: 'awg-manager',
      value: audit.awgmgr_version,
      sub: audit.awgmgr_running === false ? 'служба не работает' : 'работает',
      tone: audit.awgmgr_running === false ? 'danger' : 'ok',
    })
  }
  if (audit.hrneo_installed) {
    rows.push({
      key: 'hrneo',
      title: 'Обход блокировок',
      code: 'HydraRoute Neo',
      value: audit.hrneo_version || 'установлен',
      sub: audit.hrneo_running ? 'работает' : 'установлен, но не работает',
      tone: audit.hrneo_running ? 'ok' : 'warn',
    })
  }
  if (audit.firmware_current) {
    const update = audit.firmware_avail && audit.firmware_avail !== audit.firmware_current
    rows.push({
      key: 'firmware',
      title: 'Прошивка роутера',
      code: 'KeeneticOS',
      value: audit.firmware_current,
      // Доступное обновление -- новость, и говорится она значением строки, а
      // не примечанием, которое никто не прочтёт.
      sub: update ? `доступна ${audit.firmware_avail}` : 'свежая',
      tone: update ? 'warn' : 'ok',
    })
  }
  return rows
}

// Доктор отвечает строками "✅ имя: подробность". Экран разбирает их в
// строки данных: emoji -- это тон, имя -- фраза, подробность -- значение.
const DOCTOR_TONE = { '✅': 'ok', '⚠️': 'warn', '⚠': 'warn', '❌': 'danger' }
const VERDICT = { ok: 'в порядке', warn: 'внимание', danger: 'не работает' }

function doctorSplit(body) {
  const colon = body.indexOf(': ')
  const dash = body.indexOf(' — ')
  if (colon >= 0 && (dash < 0 || colon < dash)) return [colon, 2]
  if (dash >= 0) return [dash, 3]
  return [-1, 0]
}

export function doctorRows(output) {
  const lines = String(output ?? '').split('\n')
  const rows = []
  for (const line of lines) {
    const trimmed = line.trim()
    const mark = Object.keys(DOCTOR_TONE).find((m) => trimmed.startsWith(m))
    if (!mark) continue
    const tone = DOCTOR_TONE[mark]
    const body = trimmed.slice(mark.length).trim()
    // Агент пишет «имя: подробность» (router_doctor.go formatDetail), и в
    // подробности бывают свои двоеточия -- режем по первому. Длинное тире --
    // запасной разделитель старого формата; побеждает тот, что раньше.
    const [at, sepLen] = doctorSplit(body)
    const title = at >= 0 ? body.slice(0, at).trim() : body
    const detail = at >= 0 ? body.slice(at + sepLen).trim() : ''
    rows.push({ key: `d${rows.length}`, title, value: detail || VERDICT[tone], tone })
  }
  return rows
}

// Проверка связи читается из проекции туннеля (ping_check_status,
// ping_latency_ms), а не из ответа pingcheck_status: тот несёт ndms_name
// каждого туннеля -- топологию, которой мини-аппу не положено.
export function pingRows(tunnels) {
  return (tunnels ?? []).map((t) => {
    const status = (t.ping_check_status ?? '').trim().toLowerCase()
    let enabled = null
    let value = 'неизвестно'
    if (status === 'disabled' || status === 'off') {
      enabled = false
      value = 'выключена'
    } else if (status === 'ok' || status === 'alive') {
      enabled = true
      value = t.ping_latency_ms != null ? `${t.ping_latency_ms} мс` : 'отвечает'
    } else if (status === 'fail' || status === 'dead') {
      enabled = true
      value = 'не отвечает'
    }
    return {
      key: t.tunnel_id,
      tunnelID: t.tunnel_id,
      title: t.name || t.tunnel_id,
      code: t.tunnel_id,
      value,
      enabled,
      tone: enabled === false ? 'muted' : value === 'не отвечает' ? 'danger' : enabled ? 'ok' : 'muted',
    }
  })
}

// Прошивка (wire.FirmwareStatus). Установка необратима и перезагружает
// роутер, поэтому экран сначала показывает, ЧТО стоит и ЧТО доступно, и
// только потом предлагает действие -- да и то владельцу.
export function firmwareStatus(output) {
  let fw = null
  try {
    fw = JSON.parse(output)
  } catch {
    return { known: false, updateAvailable: false, rows: [] }
  }
  if (!fw || typeof fw !== 'object' || !fw.current) {
    return { known: false, updateAvailable: false, rows: [] }
  }
  const update = Boolean(fw.available && fw.available !== fw.current)
  const rows = [
    { key: 'current', title: 'Сейчас стоит', code: 'KeeneticOS', value: fw.current },
    {
      key: 'available',
      title: 'Роутер предлагает',
      code: fw.channel || 'канал не назван',
      // Отсутствие обновления -- это ответ, а не пустая строка: строка без
      // значения читается как «не проверяли».
      value: update ? fw.available : 'обновления нет',
      tone: update ? 'warn' : 'ok',
    },
  ]
  // Подсказка роутера -- фраза, а не значение, и строкой данных она быть не
  // может: в правой колонке значение не переносится, и длинная фраза налезет
  // на собственный заголовок. Экран печатает её отдельной строкой под
  // карточкой.
  return {
    known: true,
    updateAvailable: update,
    rows,
    current: fw.current,
    available: fw.available ?? '',
    hint: fw.hint ?? '',
  }
}

// Адрес панели awg-manager. С v0.41 сервер отдаёт его владельцу роутера и
// админу (panel_url); оператору роутера -- нет, и строки у него нет вовсе.
// Открывается напрямую во внешнем браузере: панель спросит свой логин.
// Принимаются только http(s): иной адрес в ответе -- не панель.
export function panelLink(url) {
  const raw = String(url ?? '').trim()
  if (!raw) return ''
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : ''
  } catch {
    return ''
  }
}

// Хост -- то, что человек узнаёт (awg.example.com), без схемы и пути.
export function panelHost(url) {
  const link = panelLink(url)
  return link ? new URL(link).host : ''
}

export const PANEL_PRIVATE_HINT = 'откроется только из домашней сети'

// Строка «Панель роутера» в «Управлении»: адрес известен -- хост и
// подсказка про частный адрес; нет -- честно «не сохранён».
export function panelRow(settings) {
  const url = panelLink(settings?.panel_url)
  if (!url) {
    return {
      known: false,
      url: '',
      host: '',
      hint: 'Мы не знаем адрес панели этого роутера, поэтому открыть её из приложения нельзя.',
    }
  }
  return {
    known: true,
    url,
    host: panelHost(url),
    hint: settings.panel_scope === 'private' ? PANEL_PRIVATE_HINT : '',
  }
}
