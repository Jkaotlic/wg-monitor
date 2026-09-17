// «Пакеты по расписанию»: обновление пакетов Entware по cron и очистка
// Entware. Экран перенесён из операторского дашборда (спека, п. 9). Здесь
// только чистые функции -- экран собирает из них карточки.
//
// Любое действие агента (status/install/logs/remove/run) отвечает свежим
// статусом (wire.OpkgCronStatus / wire.EntwareCleanStatus): карточка после
// каждого ответа просто перерисовывает состояние.
import { AGENT_OLDER_THAN_APP } from './labels.js'
import { MAINT_TEXTS } from './maintenance.js'
import { stampText } from './stamp.js'

export const LOG_LINES = 100
export const STATUS_LINES = 40

export const PACKAGE_JOBS = {
  opkg: {
    prefix: 'opkg_cron',
    title: 'Обновление пакетов по расписанию',
    about: 'Роутер сам обновляет списки и пакеты Entware в заданное время, если на накопителе хватает места.',
    defaultTime: '04:30',
    canRun: false,
  },
  clean: {
    prefix: 'entware_clean',
    title: 'Очистка Entware',
    about: 'Роутер по расписанию чистит временные файлы и кэш Entware и следит за свободным местом и памятью.',
    defaultTime: '05:15',
    canRun: true,
  },
}

const VERBS = new Set(['status', 'install', 'logs', 'remove'])

export function packagesAction(kind, verb) {
  const job = PACKAGE_JOBS[kind]
  if (!job) return ''
  if (VERBS.has(verb) || (verb === 'run' && job.canRun)) return `${job.prefix}_${verb}`
  return ''
}

export function normalizeHHMM(value) {
  const m = /^(\d{1,2}):(\d{2})(?::\d{2})?$/.exec(String(value ?? '').trim())
  if (!m) return ''
  const h = Number(m[1])
  const min = Number(m[2])
  if (h > 23 || min > 59) return ''
  return `${String(h).padStart(2, '0')}:${String(min).padStart(2, '0')}`
}

export function packagesArgs(kind, verb, time) {
  if (verb === 'install') return { schedule: normalizeHHMM(time) }
  if (verb === 'logs') return { lines: LOG_LINES }
  if (verb === 'status') return { lines: STATUS_LINES }
  return {}
}

// Ежедневное «м ч * * *» -- время; любое другое расписание (его мог
// поставить старый дашборд пятью полями) экран называет как есть.
export function cronToHHMM(schedule) {
  const f = String(schedule ?? '').trim().split(/\s+/)
  if (f.length !== 5 || f[2] !== '*' || f[3] !== '*' || f[4] !== '*') return ''
  if (!/^\d{1,2}$/.test(f[0]) || !/^\d{1,2}$/.test(f[1])) return ''
  return normalizeHHMM(`${f[1]}:${f[0].padStart(2, '0')}`)
}

export function formatKB(kb) {
  const n = Number(kb)
  if (!Number.isFinite(n) || n <= 0) return ''
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1).replace('.', ',')} ГБ`
  if (n >= 1024) return `${Math.round(n / 1024)} МБ`
  return `${Math.round(n)} КБ`
}

const numOr0 = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : 0)

export function parsePackagesStatus(result) {
  if (result?.status !== 'ok' || typeof result.output !== 'string') return null
  let v
  try {
    v = JSON.parse(result.output)
  } catch {
    return null
  }
  if (!v || typeof v !== 'object' || typeof v.installed !== 'boolean') return null
  const schedule = typeof v.schedule === 'string' ? v.schedule : ''
  return {
    installed: v.installed,
    schedule,
    time: cronToHHMM(schedule),
    freeKB: numOr0(v.free_kb),
    minFreeKB: numOr0(v.min_free_kb),
    memAvailableKB: numOr0(v.mem_available_kb),
    lastRun: typeof v.last_run === 'string' ? v.last_run : '',
    lastStatus: typeof v.last_status === 'string' ? v.last_status : '',
    lastFreedKB: numOr0(v.last_freed_kb),
    logTail: typeof v.log_tail === 'string' ? v.log_tail : '',
  }
}

const LAST_WORD = { ok: 'прошёл', skipped: 'пропущен', err: 'с ошибкой' }
const LAST_TONE = { skipped: 'warn', err: 'danger' }

function withTone(row, tone) {
  return tone ? { ...row, tone } : row
}

export function packagesStatusRows(status, kind, opts = {}) {
  if (!status) return []
  const rows = []
  const state = status.installed
    ? status.time
      ? `каждый день в ${status.time}`
      : `своё расписание: ${status.schedule}`
    : 'выключено'
  rows.push(withTone({ key: 'state', title: 'Состояние', value: state }, status.installed ? '' : 'warn'))

  const when = stampText(status.lastRun, opts)
  rows.push(
    when
      ? withTone(
          { key: 'last', title: 'Последний запуск', value: `${when} · ${LAST_WORD[status.lastStatus] ?? 'итог неизвестен'}` },
          LAST_TONE[status.lastStatus],
        )
      : { key: 'last', title: 'Последний запуск', value: 'ещё не запускалось' },
  )

  if (status.freeKB > 0) {
    const need = status.minFreeKB > 0 ? ` · нужно от ${formatKB(status.minFreeKB)}` : ''
    const low = status.minFreeKB > 0 && status.freeKB < status.minFreeKB
    rows.push(withTone({ key: 'space', title: 'Свободно на накопителе', value: `${formatKB(status.freeKB)}${need}` }, low ? 'warn' : ''))
  }
  if (kind === 'clean' && status.memAvailableKB > 0) {
    rows.push({ key: 'mem', title: 'Свободная память', value: formatKB(status.memAvailableKB) })
  }
  if (kind === 'clean' && status.lastFreedKB > 0) {
    rows.push({ key: 'freed', title: 'Освобождено в прошлый раз', value: formatKB(status.lastFreedKB) })
  }
  return rows
}

export function packagesOutcomeText(kind, verb, result) {
  if (!result) return ''
  if (result.status === 'timeout') return 'Роутер не ответил вовремя — проверьте состояние ещё раз.'
  if (result.status === 'locked') return MAINT_TEXTS.busy
  if (/^unknown action:/i.test(String(result.output ?? '').trim())) return AGENT_OLDER_THAN_APP
  if (result.status !== 'ok') return 'Роутер ответил ошибкой — подробности ниже.'
  const status = parsePackagesStatus(result)
  if (!status) return 'Роутер ответил непонятно — проверьте состояние ещё раз.'
  switch (verb) {
    case 'install':
      return status.time ? `Расписание сохранено: каждый день в ${status.time}.` : 'Расписание сохранено.'
    case 'remove':
      return 'Расписание снято.'
    case 'run':
      return status.lastFreedKB > 0 ? `Очистка выполнена, освобождено ${formatKB(status.lastFreedKB)}.` : 'Очистка выполнена.'
    default:
      return ''
  }
}

const BUSY = {
  install: 'Ставим расписание — на роутере это до пяти минут…',
  run: 'Чистим Entware…',
  logs: 'Читаем журнал…',
  remove: 'Снимаем расписание…',
  status: 'Спрашиваем роутер…',
}

export function packagesBusyText(verb) {
  return BUSY[verb] ?? BUSY.status
}

// Установка ставит cron через opkg (агент даёт ей до 300 с), очистка
// работает с диском; спящему роутеру -- ещё пять минут, как у обслуживания.
export function packagesDeadlineMs(verb, asleep) {
  const base = verb === 'install' || verb === 'run' ? 6 * 60_000 : 90_000
  return asleep ? base + 5 * 60_000 : base
}
