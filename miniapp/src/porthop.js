// «Смена порта при блокировке» (v0.57): чистые функции экрана.
//
// На роутере работает сторож (internal/agent/routerscripts/awg-porthop.sh):
// когда фильтр провайдера убивает поток VPN-туннеля, роутер меняет свой
// исходящий порт, и VPN-туннель оживает. Сторожит он VPN-туннели, которые
// несут весь трафик (маршрут 0.0.0.0/0): без полного маршрута его проверка
// через туннель не прошла бы никогда.
//
// Любое действие агента (porthop_status/install/remove/logs) отвечает свежим
// wire.PorthopStatus -- экран после каждого ответа перерисовывает состояние.
// Отказ «есть ручная копия» -- status "err", вывод начинается с
// legacy_running: (actions.PorthopLegacyRunningCode).
import { agentAtLeast } from './agentConfig.js'
import { AGENT_OLDER_THAN_APP } from './labels.js'
import { MAINT_TEXTS } from './maintenance.js'

export const PORTHOP_MIN_VERSION = 'v0.57.0'
export const PORTHOP_LOG_LINES = 100
const LEGACY_CODE = 'legacy_running'
const LEGACY_HOME = '/opt/etc/init.d'

export const PORTHOP_TEXTS = {
  title: 'Смена порта при блокировке',
  about:
    'Бывает, что фильтр провайдера обрывает соединение VPN-туннеля, и трафик перестаёт ходить. Тогда роутер сам меняет свой исходящий порт — и VPN-туннель снова поднимается. Сторож следит за VPN-туннелями, через которые идёт весь трафик, и проверяет их каждые 20 секунд.',
  limits: 'После трёх неудачных смен подряд сторож делает паузу на 10 минут и меняет порт не чаще 6 раз в час.',
  tooOld: 'Смена порта при блокировке появится после обновления агента на роутере до v0.57.',
  adminOnly: 'Смену порта на роутере включает админ бота.',
  unknown: 'Состояние ещё не проверено.',
}

// Кому экран доступен: админ бота и агент от v0.57.0. Отказ по умолчанию:
// пустая, нечитаемая и предрелизная версия запрещают.
export function porthopAvailable(settings) {
  if (!settings || settings.role !== 'admin') return false
  return agentAtLeast(settings.agent_version, PORTHOP_MIN_VERSION)
}

const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const str = (v) => (typeof v === 'string' ? v : '')
const strs = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === 'string' && x !== '') : [])

export function parsePorthopStatus(result) {
  if (result?.status !== 'ok' || typeof result.output !== 'string') return null
  let v
  try {
    v = JSON.parse(result.output)
  } catch {
    return null
  }
  if (!v || typeof v !== 'object' || typeof v.installed !== 'boolean') return null
  const l = v.legacy && typeof v.legacy === 'object' ? v.legacy : {}
  return {
    installed: v.installed,
    running: v.running === true,
    auto: v.auto === true,
    ifaces: strs(v.ifaces),
    watched: strs(v.watched),
    legacy: { found: l.found === true, running: l.running === true, path: str(l.path), movedTo: str(l.moved_to) },
    hops: num(v.hops_24h),
    recovered: num(v.recovered_24h),
    failed: num(v.failed_24h),
    lastEvent: str(v.last_event),
    logTail: str(v.log_tail),
    scriptPath: str(v.script_path),
    confPath: str(v.conf_path),
    logPath: str(v.log_path),
  }
}

// porthopFailure -- почему ответ не статус. null -- ответ не отказ.
export function porthopFailure(result) {
  if (!result) return null
  if (result.status === 'timeout') return { kind: 'timeout' }
  if (result.status === 'locked') return { kind: 'busy' }
  if (result.status === 'ok') return null
  const out = String(result.output ?? '').trim()
  if (/^unknown action:/i.test(out)) return { kind: 'old' }
  if (out.startsWith(LEGACY_CODE)) {
    const m = /\((\/[^)\s]+)\)/.exec(out)
    return { kind: 'legacy', path: m ? m[1] : '' }
  }
  return { kind: 'err' }
}

export function porthopLegacyText(path) {
  const where = path ? ` (${path})` : ''
  return `На роутере уже стоит ручная копия смены порта${where}. Две копии мешали бы друг другу на одном VPN-туннеле. Ничего не изменено. «Заменить ручную копию» остановит её и уберёт из автозапуска, а файл сохранит — её можно вернуть.`
}

// То же до нажатия: статус нашёл ручную копию (работает или запустится при
// загрузке) -- экран объясняет заранее, почему вместо «Включить» замена.
export function porthopLegacyFoundText(legacy) {
  const where = legacy?.path ? ` (${legacy.path})` : ''
  const state = legacy?.running ? 'работает' : 'стоит и запустится при перезагрузке'
  return `На роутере уже ${state} ручная копия смены порта${where}. Вторую рядом не ставим: две копии мешали бы друг другу на одном VPN-туннеле. «Заменить ручную копию» остановит её и уберёт из автозапуска, а файл сохранит — её можно вернуть.`
}

// Имя VPN-туннеля по интерфейсу -- из снимка route_status (кеш вкладки
// «VPN-туннели»), как во всём приложении. Нет снимка или туннеля в нём --
// имя интерфейса: выдумать имя нечем, а молчать хуже.
export function watchedNames(ifaces, snapshot) {
  const byIface = new Map()
  for (const t of Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []) {
    const iface = String(t?.iface ?? '').trim().toLowerCase()
    const name = String(t?.name || t?.id || '').trim()
    if (iface && name) byIface.set(iface, name)
  }
  return (ifaces ?? []).map((i) => byIface.get(String(i).toLowerCase()) ?? i)
}

// Строка журнала сторожа: «2026-10-07 12:00:00 +0300 opkgtun10: …» (с
// v0.57 -- со сдвигом пояса роутера; старые строки -- без него). Сдвиг
// человеку не нужен: время -- роутерное, как в журнале.
export function lastEventText(line) {
  return String(line ?? '').replace(/^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) [+-]\d{4} /, '$1 ')
}

function plural(n, one, few, many) {
  const m10 = n % 10
  const m100 = n % 100
  if (m10 === 1 && m100 !== 11) return one
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few
  return many
}

function withTone(row, tone) {
  return tone ? { ...row, tone } : row
}

export function porthopStatusRows(status, snapshot = null) {
  if (!status) return []
  const rows = []
  const state = status.installed ? (status.running ? 'включено, работает' : 'включено, но не работает') : 'выключено'
  rows.push(withTone({ key: 'state', title: 'Состояние', value: state }, status.installed ? (status.running ? '' : 'danger') : 'warn'))

  const names = watchedNames(status.watched, snapshot)
  const watchedTitle = status.installed ? 'Сторожит' : 'Будет сторожить'
  if (names.length === 0) {
    rows.push({ key: 'watched', title: watchedTitle, value: 'нет VPN-туннеля, который несёт весь трафик', tone: 'warn' })
  } else {
    const manual = !status.auto && status.ifaces.length > 0 ? ' (заданы вручную)' : ''
    rows.push({ key: 'watched', title: watchedTitle, value: `${names.join(', ')}${manual}` })
  }

  if (status.installed) {
    const day =
      status.hops === 0
        ? 'смен порта не было'
        : `${status.hops} ${plural(status.hops, 'смена', 'смены', 'смен')} порта: ожили ${status.recovered}, не ожили ${status.failed}`
    rows.push(withTone({ key: 'day', title: 'За сутки', value: day }, status.failed > 0 && status.recovered === 0 ? 'warn' : ''))
    if (status.lastEvent) rows.push({ key: 'last', title: 'Последнее событие', value: lastEventText(status.lastEvent) })
  }

  if (status.legacy.found) {
    const value = status.legacy.running ? 'работает' : 'включится при перезагрузке'
    rows.push({ key: 'legacy', title: 'Ручная копия', value: status.legacy.path ? `${value} · ${status.legacy.path}` : value, tone: 'warn' })
  }
  return rows
}

// Какие кнопки рисовать. install -- подпись кнопки установки ('' -- нет);
// replace -- «Заменить ручную копию» (вместо установки: простая установка
// упрётся в legacy_running). Без известного состояния ничего не ставим
// вслепую: только «Проверить» и «Журнал».
export function porthopButtons(status, failure = null) {
  if (!status) {
    if (failure?.kind === 'legacy') return { install: '', remove: false, replace: true }
    return { install: '', remove: false, replace: false }
  }
  const legacy = failure?.kind === 'legacy' || status.legacy.found
  if (legacy) return { install: '', remove: status.installed, replace: true }
  if (!status.installed) return { install: 'Включить', remove: false, replace: false }
  return { install: status.running ? '' : 'Включить снова', remove: true, replace: false }
}

// Действие агента по глаголу экрана: replace -- та же установка с заменой
// ручной копии.
export function porthopAction(verb) {
  return verb === 'replace' ? 'porthop_install' : `porthop_${verb}`
}

export function porthopArgs(verb) {
  if (verb === 'replace') return { replace_legacy: true }
  if (verb === 'logs') return { lines: PORTHOP_LOG_LINES }
  return {}
}

// Установка и снятие на агенте -- до 60 с; спящему роутеру -- ещё пять минут.
export function porthopDeadlineMs(verb, asleep) {
  const base = verb === 'install' || verb === 'replace' || verb === 'remove' ? 2 * 60_000 : 90_000
  return asleep ? base + 5 * 60_000 : base
}

const BUSY = {
  install: 'Ставим смену порта…',
  replace: 'Заменяем ручную копию…',
  remove: 'Выключаем смену порта…',
  logs: 'Читаем журнал…',
  status: 'Спрашиваем роутер…',
}

export function porthopBusyText(verb) {
  return BUSY[verb] ?? BUSY.status
}

export function porthopOutcomeText(verb, result) {
  if (!result) return ''
  const fail = porthopFailure(result)
  if (fail) {
    switch (fail.kind) {
      case 'timeout':
        return 'Роутер не ответил вовремя — проверьте состояние ещё раз.'
      case 'busy':
        return MAINT_TEXTS.busy
      case 'old':
        return AGENT_OLDER_THAN_APP
      case 'legacy':
        return porthopLegacyText(fail.path)
      default:
        return 'Роутер ответил ошибкой — подробности ниже.'
    }
  }
  const status = parsePorthopStatus(result)
  if (!status) return 'Роутер ответил непонятно — проверьте состояние ещё раз.'
  switch (verb) {
    case 'install':
    case 'replace': {
      const base = status.running ? 'Смена порта включена.' : 'Смена порта поставлена, но не запустилась — посмотрите журнал.'
      if (!status.legacy.movedTo) return base
      return `${base} Ручная копия перенесена в ${status.legacy.movedTo}; вернуть — перенести файл обратно в ${LEGACY_HOME}.`
    }
    case 'remove':
      return 'Смена порта выключена.'
    default:
      return ''
  }
}
