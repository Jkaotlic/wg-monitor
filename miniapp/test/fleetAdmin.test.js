import { describe, it, expect } from 'vitest'
import {
  EMPTY_PARK,
  fleetHeadline,
  backendRow,
  fleetRouterRows,
  notifyGapLines,
  watchdogLine,
  webLinkLines,
} from '../src/fleetAdmin.js'

const FLEET = {
  generated_at: '2026-09-12T10:00:00Z',
  totals: { routers: 3, online: 1, sleeping: 1, offline: 0, alerts: 1, pending_deploys: 1 },
  backend: { version: 'v0.31.0', latest_version: 'v0.31.1', update_available: true },
  routers: [
    {
      id: 1,
      nickname: 'Дом',
      status: 'alert',
      last_seen_age_sec: 90,
      incidents: ['dns'],
      agent_version: 'v0.30.0',
      awgmgr_version: '2.18.2',
      firmware_current: '4.2.7',
      update_hint: 'пора обновить: прошивка 4.3.0',
    },
    { id: 2, nickname: 'Дача', status: 'sleeping', last_seen_age_sec: 4000, agent_version: 'v0.30.0' },
    {
      id: 3,
      nickname: 'Офис',
      status: 'online',
      last_seen_age_sec: 30,
      agent_version: 'v0.29.0',
      pending_version: 'v0.30.0',
    },
  ],
  notify: {
    unreachable: [{ telegram_user_id: 100, last_error: 'bot was blocked by the user', updated_at: '2026-09-12T09:00:00Z' }],
    routers_without_recipients: ['Склад'],
  },
  watchdog: { alive: true, last_scan_at: '2026-09-12T09:59:00Z', offline_errors: 2, last_offline_error: 'forbidden', last_offline_error_router: 'Дача' },
}

describe('строка о парке', () => {
  it('считает состояния, а не просто число роутеров', () => {
    const line = fleetHeadline(FLEET)
    expect(line).toContain('3')
    expect(line).toContain('на связи')
    expect(line).toContain('спит')
    expect(line).toContain('тревог')
  })

  it('пустой парк -- это строка, а не пустой экран', () => {
    expect(fleetHeadline({ totals: { routers: 0 }, routers: [] })).toBe(EMPTY_PARK)
    expect(fleetRouterRows({ routers: [] })).toEqual([])
    expect(fleetHeadline(undefined)).toBe(EMPTY_PARK)
  })
})

describe('строка о бэкенде', () => {
  it('говорит свою версию и что доступна новее', () => {
    const row = backendRow(FLEET)
    expect(row.value).toBe('v0.31.0')
    expect(row.sub).toContain('v0.31.1')
  })

  it('без новой версии не выдумывает вторую строку', () => {
    const row = backendRow({ backend: { version: 'v0.31.0', update_available: false } })
    expect(row.value).toBe('v0.31.0')
    expect(row.sub).toBe('')
  })
})

describe('строки роутеров', () => {
  const rows = fleetRouterRows(FLEET)

  it('состояние сказано словом, а не цветом', () => {
    expect(rows[0].state).toBe('есть тревога')
    expect(rows.find((r) => r.id === 2).state).toBe('спит')
    expect(rows.find((r) => r.id === 3).state).toBe('работает')
  })

  it('сломанное сверху: тревога впереди спящего и живого', () => {
    expect(rows.map((r) => r.id)).toEqual([1, 2, 3])
  })

  it('тревога названа по-русски, а не именем проверки', () => {
    expect(rows[0].sub).not.toContain('dns')
    expect(rows[0].sub.length).toBeGreaterThan(0)
  })

  it('молчащий роутер говорит, сколько он не на связи', () => {
    expect(rows.find((r) => r.id === 2).sub).toMatch(/не на связи|отчёт/)
  })

  it('версии агента, панели и pending видны строкой', () => {
    expect(rows[0].versions).toContain('v0.30.0')
    expect(rows[0].versions).toContain('2.18.2')
    expect(rows.find((r) => r.id === 3).versions).toContain('v0.30.0')
  })

  it('«пора обновить» приходит с сервера и не пересчитывается в клиенте', () => {
    expect(rows[0].hint).toBe('пора обновить: прошивка 4.3.0')
    expect(rows.find((r) => r.id === 2).hint).toBe('')
  })
})

describe('дыры уведомлений', () => {
  it('говорят последствие, а не код ошибки Telegram', () => {
    const lines = notifyGapLines(FLEET)
    expect(lines.join('\n')).toContain('не получит тревогу, пока сам не напишет боту')
    expect(lines.join('\n')).not.toContain('bot was blocked')
  })

  it('роутер без адресатов назван по имени', () => {
    const lines = notifyGapLines(FLEET)
    expect(lines.join('\n')).toContain('Склад')
    expect(lines.join('\n')).toContain('некому')
  })

  it('дыр нет -- строк нет, а не «всё хорошо» пустым списком', () => {
    expect(notifyGapLines({ notify: { unreachable: [], routers_without_recipients: [] } })).toEqual([])
    expect(notifyGapLines({})).toEqual([])
  })
})

describe('сторож парка', () => {
  it('говорит, когда был обход и сколько отправок сорвалось', () => {
    const line = watchdogLine(FLEET)
    expect(line).toContain('обход')
    expect(line).toContain('2')
  })

  it('сторожа в сборке нет -- строки нет, а не «сторож мёртв»', () => {
    expect(watchdogLine({})).toBe('')
  })
})

describe('ссылка в браузер', () => {
  it('срок и лимит берутся из ответа сервера -- второй копии текста нет', () => {
    const lines = webLinkLines({
      url: 'https://wg.example.com/dashboard/login#token=deadbeef',
      notice: 'Ссылка личная и живёт 12 часов. Не пересылайте её: по ней всё это время открывается управление всем парком.',
      limit_notice: 'Живыми остаются три последние ссылки: выдали новую — самая старая перестала работать.',
    })
    expect(lines).toHaveLength(2)
    expect(lines[0]).toContain('12 часов')
    expect(lines[1]).toContain('три последние ссылки')
  })

  it('ссылку саму на экране не повторяем: она уже ушла в браузер', () => {
    const lines = webLinkLines({ url: 'https://wg.example.com/dashboard/login#token=deadbeef' })
    expect(lines.join(' ')).not.toContain('#token=')
  })
})
