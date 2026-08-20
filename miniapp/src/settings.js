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

export function thresholdRows(settings) {
  if (!settings) return []
  const rows = []
  if (settings.silence_after_sec) {
    rows.push({
      key: 'silence',
      title: 'Считаем, что роутер молчит, после',
      code: 'heartbeat.stale_after_sec',
      value: `${humanAge(settings.silence_after_sec)} без отчёта`,
    })
  }
  // Только у мобильного: у статичного «молчит» и «выключен» -- одно событие,
  // и вторая строка была бы копией первой другими словами.
  if (settings.mobile && settings.offline_after_sec) {
    rows.push({
      key: 'offline',
      title: 'Считаем выключенным после',
      code: 'mobile_offline_after_sec',
      value: `${humanAge(settings.offline_after_sec)} молчания`,
    })
  }
  if (settings.alert_after_fails) {
    rows.push({
      key: 'alert',
      title: 'Тревога, если проверка провалилась',
      code: 'state.fail_threshold',
      value: checksCount(settings.alert_after_fails),
    })
  }
  if (settings.recovery_after_oks) {
    rows.push({
      key: 'recovery',
      title: 'Отбой, если снова в порядке',
      code: 'state.recovery_threshold',
      value: checksCount(settings.recovery_after_oks),
    })
  }
  if (settings.agent_version) {
    rows.push({
      key: 'agent',
      title: 'Агент на роутере',
      code: 'last_deployed_version',
      value: settings.agent_version,
    })
  }
  return rows
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
      code: 'HR Neo',
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

// Доктор отвечает строками "✅ имя — подробность". Экран разбирает их в
// строки данных: emoji -- это тон, имя -- фраза, подробность -- значение.
const DOCTOR_TONE = { '✅': 'ok', '⚠️': 'warn', '⚠': 'warn', '❌': 'danger' }
const VERDICT = { ok: 'в порядке', warn: 'внимание', danger: 'не работает' }

export function doctorRows(output) {
  const lines = String(output ?? '').split('\n')
  const rows = []
  for (const line of lines) {
    const trimmed = line.trim()
    const mark = Object.keys(DOCTOR_TONE).find((m) => trimmed.startsWith(m))
    if (!mark) continue
    const tone = DOCTOR_TONE[mark]
    const body = trimmed.slice(mark.length).trim()
    // Разделитель у агента -- длинное тире с пробелами (formatDetail).
    const at = body.indexOf(' — ')
    const title = at >= 0 ? body.slice(0, at).trim() : body
    const detail = at >= 0 ? body.slice(at + 3).trim() : ''
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
