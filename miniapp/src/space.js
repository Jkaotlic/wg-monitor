// «Свободное место» (v0.57): чистые функции экрана.
//
// space_report отвечает wire.SpaceReport: свободно/всего на /opt и
// крупнейшие каталоги (du -x -k -d 2 /opt, первые 10). Только чтение. top
// бывает пуст, когда du не отработал, -- экран говорит это словами.
//
// «Почистить сейчас» -- entware_clean_run: с v0.57 работает и без
// расписания. Сколько освобождено, экран считает по месту до и после (два
// отчёта space_report), а не верит одному счёту очистки: место до чистки --
// то, что человек видел на экране.
import { agentAtLeast } from './agentConfig.js'
import { AGENT_OLDER_THAN_APP } from './labels.js'
import { MAINT_TEXTS } from './maintenance.js'
import { formatKB, parsePackagesStatus } from './packagesSchedule.js'

export const SPACE_MIN_VERSION = 'v0.57.0'

export const SPACE_TEXTS = {
  title: 'Свободное место',
  about:
    'Сколько места осталось на накопителе Entware и чем оно занято. Когда места нет, перестают обновляться пакеты и могут не запуститься службы роутера.',
  topTitle: 'Больше всего занимают',
  topUnknown: 'Не удалось узнать, чем занято место.',
  cleanTitle: 'Очистка сейчас',
  cleanAbout: 'Очистка убирает временные файлы старше суток и кэш пакетов Entware. Настройки и установленные пакеты не трогает.',
  cleanButton: 'Почистить сейчас',
  tooOld: 'Отчёт о месте и очистка по кнопке появятся после обновления агента на роутере до v0.57.',
  adminOnly: 'Место на роутере смотрит админ бота.',
  unknown: 'Место ещё не проверено.',
}

const TIMEOUT = 'Роутер не ответил вовремя — проверьте место ещё раз.'

export function spaceAvailable(settings) {
  if (!settings || settings.role !== 'admin') return false
  return agentAtLeast(settings.agent_version, SPACE_MIN_VERSION)
}

const isNum = (v) => typeof v === 'number' && Number.isFinite(v)

export function parseSpaceReport(result) {
  if (result?.status !== 'ok' || typeof result.output !== 'string') return null
  let v
  try {
    v = JSON.parse(result.output)
  } catch {
    return null
  }
  if (!v || typeof v !== 'object' || !isNum(v.free_kb) || !isNum(v.total_kb)) return null
  const top = (Array.isArray(v.top) ? v.top : [])
    .filter((e) => e && typeof e.path === 'string' && e.path !== '' && isNum(e.kb))
    .map((e) => ({ path: e.path, kb: e.kb }))
  return { freeKB: v.free_kb, totalKB: v.total_kb, top }
}

export function spaceSummaryRows(report) {
  if (!report) return []
  if (report.freeKB <= 0) return [{ key: 'free', title: 'Свободно на накопителе', value: 'нет свободного места', tone: 'danger' }]
  const total = report.totalKB > 0 ? ` из ${formatKB(report.totalKB)} (${Math.round((report.freeKB / report.totalKB) * 100)}%)` : ''
  const row = { key: 'free', title: 'Свободно на накопителе', value: `${formatKB(report.freeKB)}${total}` }
  if (report.totalKB > 0 && report.freeKB < report.totalKB / 10) row.tone = 'warn'
  return [row]
}

export function spaceTopRows(report) {
  if (!report) return []
  return report.top.map((e) => ({ key: e.path, title: e.path, value: formatKB(e.kb) || '0 КБ' }))
}

// Отказ space_report словами; '' -- ответ годный.
export function spaceFailureText(result) {
  if (!result) return ''
  if (result.status === 'timeout') return TIMEOUT
  if (result.status === 'locked') return MAINT_TEXTS.busy
  if (result.status !== 'ok') {
    if (/^unknown action:/i.test(String(result.output ?? '').trim())) return AGENT_OLDER_THAN_APP
    return 'Роутер не смог посчитать место — подробности ниже.'
  }
  return parseSpaceReport(result) ? '' : 'Роутер ответил непонятно — проверьте место ещё раз.'
}

// Итог «Почистить сейчас». before/after -- разобранные отчёты о месте до и
// после (любой может отсутствовать: роутер не ответил на замер).
export function cleanOutcomeText({ run, before = null, after = null } = {}) {
  if (!run) return ''
  if (run.status === 'timeout') return TIMEOUT
  if (run.status === 'locked') return MAINT_TEXTS.busy
  if (run.status !== 'ok') {
    if (/^unknown action:/i.test(String(run.output ?? '').trim())) return AGENT_OLDER_THAN_APP
    return 'Роутер ответил ошибкой — подробности ниже.'
  }
  const st = parsePackagesStatus(run)
  if (st?.lastStatus === 'skipped') return 'Очистка пропущена — подробности в журнале очистки.'
  if (st?.lastStatus === 'err') return 'Очистка закончилась ошибкой — подробности в журнале очистки.'
  if (before && after) {
    const freed = after.freeKB - before.freeKB
    return freed > 0 ? `Очистка выполнена, освобождено ${formatKB(freed)}.` : 'Очистка выполнена, свободного места не прибавилось.'
  }
  // last_freed_kb очистки -- освобождённая ПАМЯТЬ (MemAvailable до и после),
  // а не место на накопителе: за «освобождено» её не выдаём.
  return 'Очистка выполнена.'
}

// Сроки: замер места -- быстрый, очистка работает с диском (как у карточки
// очистки); спящему роутеру -- ещё пять минут.
export function spaceDeadlineMs(action, asleep) {
  const base = action === 'entware_clean_run' ? 6 * 60_000 : 90_000
  return asleep ? base + 5 * 60_000 : base
}
