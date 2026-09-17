import { describe, it, expect } from 'vitest'
import {
  PACKAGE_JOBS,
  LOG_LINES,
  packagesAction,
  packagesArgs,
  normalizeHHMM,
  cronToHHMM,
  formatKB,
  parsePackagesStatus,
  packagesStatusRows,
  packagesOutcomeText,
  packagesBusyText,
  packagesDeadlineMs,
} from '../src/packagesSchedule.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'
import { MAINT_TEXTS } from '../src/maintenance.js'

const OPKG = {
  installed: true, schedule: '30 4 * * *', script_path: '/opt/bin/wgm-opkg-cron', log_path: '/opt/var/log/wgm-opkg.log',
  cron_service: 'running', free_kb: 512000, total_kb: 1024000, min_free_kb: 102400,
  last_run: '2026-09-17T01:30:00Z', last_status: 'ok', log_tail: '2026-09-17T01:30:00Z status=ok',
}
const CLEAN = {
  installed: false, script_path: '/opt/bin/wgm-entware-clean', log_path: '/opt/var/log/wgm-clean.log',
  free_kb: 51200, min_free_kb: 102400, mem_available_kb: 60000, mem_total_kb: 250000, last_freed_kb: 2048,
}
const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })

describe('действия и аргументы', () => {
  it('имена действий агента; «Запустить сейчас» -- только у очистки', () => {
    expect(packagesAction('opkg', 'status')).toBe('opkg_cron_status')
    expect(packagesAction('opkg', 'install')).toBe('opkg_cron_install')
    expect(packagesAction('opkg', 'logs')).toBe('opkg_cron_logs')
    expect(packagesAction('opkg', 'remove')).toBe('opkg_cron_remove')
    expect(packagesAction('opkg', 'run')).toBe('')
    expect(packagesAction('clean', 'run')).toBe('entware_clean_run')
    expect(packagesAction('clean', 'drop')).toBe('')
    expect(packagesAction('other', 'status')).toBe('')
    expect(PACKAGE_JOBS.opkg.canRun).toBe(false)
    expect(PACKAGE_JOBS.clean.canRun).toBe(true)
  })

  it('аргументы: время для установки, строки для журнала и статуса', () => {
    expect(packagesArgs('opkg', 'install', '3:05')).toEqual({ schedule: '03:05' })
    expect(packagesArgs('clean', 'logs', '')).toEqual({ lines: LOG_LINES })
    expect(LOG_LINES).toBe(100)
    expect(packagesArgs('opkg', 'status', '')).toEqual({ lines: 40 })
    expect(packagesArgs('clean', 'run', '05:15')).toEqual({})
    expect(packagesArgs('opkg', 'remove', '05:15')).toEqual({})
  })

  it('время по умолчанию -- как у старого дашборда', () => {
    expect(PACKAGE_JOBS.opkg.defaultTime).toBe('04:30')
    expect(PACKAGE_JOBS.clean.defaultTime).toBe('05:15')
  })
})

describe('время HH:MM', () => {
  it('нормализация и отказ', () => {
    expect(normalizeHHMM('04:30')).toBe('04:30')
    expect(normalizeHHMM('4:30')).toBe('04:30')
    expect(normalizeHHMM('23:59:00')).toBe('23:59')
    expect(normalizeHHMM('24:00')).toBe('')
    expect(normalizeHHMM('12:60')).toBe('')
    expect(normalizeHHMM('0430')).toBe('')
    expect(normalizeHHMM('')).toBe('')
  })

  it('cron «м ч * * *» -- в HH:MM, иное расписание -- пусто', () => {
    expect(cronToHHMM('30 4 * * *')).toBe('04:30')
    expect(cronToHHMM('0 23 * * *')).toBe('23:00')
    expect(cronToHHMM('*/15 * * * *')).toBe('')
    expect(cronToHHMM('30 4 * * 1')).toBe('')
    expect(cronToHHMM('')).toBe('')
  })

  it('объём по-русски', () => {
    expect(formatKB(512)).toBe('512 КБ')
    expect(formatKB(2048)).toBe('2 МБ')
    expect(formatKB(3 * 1024 * 1024)).toBe('3,0 ГБ')
    expect(formatKB(0)).toBe('')
    expect(formatKB(undefined)).toBe('')
  })
})

describe('разбор ответа агента', () => {
  it('статус -- из JSON; ошибка и мусор -- null', () => {
    expect(parsePackagesStatus(ok(OPKG))).toEqual({
      installed: true, schedule: '30 4 * * *', time: '04:30', freeKB: 512000, minFreeKB: 102400, memAvailableKB: 0,
      lastRun: '2026-09-17T01:30:00Z', lastStatus: 'ok', lastFreedKB: 0, logTail: '2026-09-17T01:30:00Z status=ok',
    })
    expect(parsePackagesStatus({ status: 'err', output: 'exec not configured' })).toBe(null)
    expect(parsePackagesStatus({ status: 'ok', output: 'not json' })).toBe(null)
    expect(parsePackagesStatus({ status: 'ok', output: '{"foo":1}' })).toBe(null)
    expect(parsePackagesStatus(null)).toBe(null)
  })

  it('строки карточки обновления пакетов', () => {
    const rows = packagesStatusRows(parsePackagesStatus(ok(OPKG)), 'opkg', { timeZone: 'UTC' })
    expect(rows).toEqual([
      { key: 'state', title: 'Состояние', value: 'каждый день в 04:30' },
      { key: 'last', title: 'Последний запуск', value: '17.09 01:30 · прошёл' },
      { key: 'space', title: 'Свободно на накопителе', value: '500 МБ · нужно от 100 МБ' },
    ])
  })

  it('строки карточки очистки: выключено, мало места, память, освобождено', () => {
    const rows = packagesStatusRows(parsePackagesStatus(ok(CLEAN)), 'clean', { timeZone: 'UTC' })
    expect(rows).toEqual([
      { key: 'state', title: 'Состояние', value: 'выключено', tone: 'warn' },
      { key: 'last', title: 'Последний запуск', value: 'ещё не запускалось' },
      { key: 'space', title: 'Свободно на накопителе', value: '50 МБ · нужно от 100 МБ', tone: 'warn' },
      { key: 'mem', title: 'Свободная память', value: '59 МБ' },
      { key: 'freed', title: 'Освобождено в прошлый раз', value: '2 МБ' },
    ])
  })

  it('своё расписание cron и итог запуска с ошибкой', () => {
    const custom = parsePackagesStatus(ok({ ...OPKG, schedule: '*/30 * * * *', last_status: 'err' }))
    const rows = packagesStatusRows(custom, 'opkg', { timeZone: 'UTC' })
    expect(rows[0]).toEqual({ key: 'state', title: 'Состояние', value: 'своё расписание: */30 * * * *' })
    expect(rows[1]).toEqual({ key: 'last', title: 'Последний запуск', value: '17.09 01:30 · с ошибкой', tone: 'danger' })
  })
})

describe('итог действия словами', () => {
  it('успехи', () => {
    expect(packagesOutcomeText('opkg', 'install', ok({ ...OPKG, schedule: '15 3 * * *' }))).toBe('Расписание сохранено: каждый день в 03:15.')
    expect(packagesOutcomeText('opkg', 'remove', ok({ ...OPKG, installed: false, schedule: '' }))).toBe('Расписание снято.')
    expect(packagesOutcomeText('clean', 'run', ok(CLEAN))).toBe('Очистка выполнена, освобождено 2 МБ.')
    expect(packagesOutcomeText('clean', 'run', ok({ ...CLEAN, last_freed_kb: 0 }))).toBe('Очистка выполнена.')
    expect(packagesOutcomeText('opkg', 'status', ok(OPKG))).toBe('')
    expect(packagesOutcomeText('opkg', 'logs', ok(OPKG))).toBe('')
  })

  it('отказы: старый агент, замок пакетов, таймаут, ошибка, мусор', () => {
    expect(packagesOutcomeText('opkg', 'status', { status: 'err', output: 'unknown action: opkg_cron_status' })).toBe(AGENT_OLDER_THAN_APP)
    expect(packagesOutcomeText('opkg', 'install', { status: 'locked', output: 'opkg busy' })).toBe(MAINT_TEXTS.busy)
    expect(packagesOutcomeText('opkg', 'install', { status: 'timeout', output: '' })).toBe('Роутер не ответил вовремя — проверьте состояние ещё раз.')
    expect(packagesOutcomeText('opkg', 'install', { status: 'err', output: 'crontab: exit 1' })).toBe('Роутер ответил ошибкой — подробности ниже.')
    expect(packagesOutcomeText('opkg', 'install', { status: 'ok', output: 'nope' })).toBe('Роутер ответил непонятно — проверьте состояние ещё раз.')
    expect(packagesOutcomeText('opkg', 'install', null)).toBe('')
  })

  it('пока идёт и сколько ждать', () => {
    expect(packagesBusyText('install')).toBe('Ставим расписание — на роутере это до пяти минут…')
    expect(packagesBusyText('status')).toBe('Спрашиваем роутер…')
    expect(packagesDeadlineMs('install', false)).toBe(6 * 60_000)
    expect(packagesDeadlineMs('run', false)).toBe(6 * 60_000)
    expect(packagesDeadlineMs('status', false)).toBe(90_000)
    expect(packagesDeadlineMs('status', true)).toBe(90_000 + 5 * 60_000)
  })
})
