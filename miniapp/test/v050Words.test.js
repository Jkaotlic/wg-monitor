import { readdirSync, readFileSync } from 'node:fs'
import { describe, it, expect } from 'vitest'
import { whenText, agoText, sinceText, untilText } from '../src/when.js'
import { watchdogLine } from '../src/watchdogLine.js'
import { SIGNAL_TEXTS } from '../src/signals.js'
import { agentConfigFields, agentConfigRows } from '../src/agentConfig.js'
import { AWG3_TEXTS, summaryText, routerPickRows, deletePanelSheetText } from '../src/awg3Panel.js'
import { warningFoldTitle } from '../src/fleetAdmin.js'
import { agentUpdateSheetText } from '../src/agentUpdate.js'
import { formatBytes, trafficView } from '../src/traffic.js'

const TZ = 'Europe/Moscow'
const NOW = Date.parse('2026-09-29T14:56:00Z') // 17:56 по Москве

describe('одно время (спека п. 3.5)', () => {
  it('сегодня / вчера / дата без года / дата с годом', () => {
    expect(whenText('2026-09-29T14:56:00Z', { now: NOW, timeZone: TZ })).toBe('сегодня, 17:56')
    expect(whenText('2026-09-28T14:56:00Z', { now: NOW, timeZone: TZ })).toBe('вчера, 17:56')
    expect(whenText('2026-09-27T14:56:00Z', { now: NOW, timeZone: TZ })).toBe('27 сен, 17:56')
    expect(whenText('2025-12-31T20:00:00Z', { now: NOW, timeZone: TZ })).toBe('31 дек 2025, 23:00')
  })

  it('граница суток -- по поясу того, кто смотрит', () => {
    expect(whenText('2026-09-28T21:30:00Z', { now: NOW, timeZone: TZ })).toBe('сегодня, 00:30')
    expect(whenText('2026-09-28T21:30:00Z', { now: NOW, timeZone: 'UTC' })).toBe('вчера, 21:30')
  })

  it('пусто и мусор -- пусто', () => {
    expect(whenText('', { now: NOW })).toBe('')
    expect(whenText(null)).toBe('')
    expect(whenText('вчера')).toBe('')
  })

  it('«с» -- без запятой: «сегодня с 17:56», а не «с сегодня, 17:56»', () => {
    expect(sinceText('2026-09-29T14:56:00Z', { now: NOW, timeZone: TZ })).toBe('сегодня с 17:56')
    expect(sinceText('2026-09-28T03:00:00Z', { now: NOW, timeZone: TZ })).toBe('вчера с 06:00')
    expect(sinceText('2026-09-27T14:56:00Z', { now: NOW, timeZone: TZ })).toBe('27 сен с 17:56')
    expect(sinceText('', { now: NOW })).toBe('')
  })

  it('«до» -- будущее без запятой: время / «завтра 09:00» / «27 сен 09:00»', () => {
    expect(untilText('2026-09-29T17:00:00Z', { now: NOW, timeZone: TZ })).toBe('20:00')
    expect(untilText('2026-09-30T06:00:00Z', { now: NOW, timeZone: TZ })).toBe('завтра 09:00')
    expect(untilText('2026-10-03T06:00:00Z', { now: NOW, timeZone: TZ })).toBe('3 окт 09:00')
    expect(untilText('2027-01-03T06:00:00Z', { now: NOW, timeZone: TZ })).toBe('3 янв 2027 09:00')
    expect(untilText('', { now: NOW })).toBe('')
  })

  it('отсчёт -- один хелпер', () => {
    expect(agoText(30)).toBe('только что')
    expect(agoText(125)).toBe('2 мин назад')
    expect(agoText(7200)).toBe('2 ч назад')
    expect(agoText(90000)).toBe('1 дн назад')
    expect(agoText(null)).toBe('')
  })
})

describe('слова (спека п. 3.4)', () => {
  it('«линия» → «VPN-туннель»', () => {
    expect(SIGNAL_TEXTS.hooksTitle).toBe('Мгновенная реакция, когда меняется VPN-туннель')
    const all = JSON.stringify([agentConfigFields(), agentConfigRows({ wake_hooks_off: false })])
    expect(all).not.toMatch(/лини/)
    expect(all).toContain('когда меняется VPN-туннель')
  })

  it('«Сторож» → «Проверка молчащих роутеров», «обход занял» убран, числа впереди', () => {
    const wd = watchdogLine({
      generated_at: '2026-09-17T10:00:40Z',
      watchdog: { alive: true, last_scan_at: '2026-09-17T10:00:00Z', scans_total: 1234, stale_users: 2, suppressed_users: 1, last_scan_ms: 85, offline_errors: 0 },
    })
    expect(wd.title).toBe('Проверка молчащих роутеров')
    expect(wd.text).toBe('последний обход только что · без отчёта: 2 · 1 заглушён')
    expect(wd.sub).toBe('1234 обхода с запуска')
    expect(JSON.stringify(wd)).not.toMatch(/Сторож|обход занял/)
  })

  it('«Оговорка к обновлению» → «Что может помешать обновлению»', () => {
    expect(warningFoldTitle(2)).toBe('Что может помешать обновлению · 2')
    const body = agentUpdateSheetText({ nickname: 'x', agent_version: 'v0.47.0', agent_update_warning: 'нужно место.' }, 'v0.49.0').body
    expect(body).toContain('Что может помешать: нужно место.')
    expect(body).not.toContain('Оговорка')
  })

  it('awg3: «пир» → «устройство», техническая запись -- «запись»', () => {
    const texts = JSON.stringify([AWG3_TEXTS, summaryText({ peers_total: 0 }), routerPickRows([{ id: 1, nickname: 'home' }, { id: 2, nickname: 'work' }], [{ router: { id: 1 } }]), deletePanelSheetText({ label: 'Main' })])
    // \b в JS -- только латиница; «пир» как отдельное слово/начало слова, но не «скопировать».
    expect(texts).not.toMatch(/(^|[^а-яё])пир/i)
    expect(summaryText({ peers_total: 0 })).toBe('устройств нет')
    expect(routerPickRows([{ id: 1, nickname: 'home' }], [{ router: { id: 1 } }])[0].sub).toBe('запись на панели уже есть — возьмём её конфиг')
  })
})

describe('числа с единицами (спека п. 3.6)', () => {
  it('узкий неразрывный пробел', () => {
    expect(formatBytes(1536)).toBe('1,5 КБ')
    expect(formatBytes(900)).toBe('900 Б')
  })

  it('обмен до запроса -- плитка-приглашение, «точек в ряду» нет', () => {
    const idle = trafficView(null)
    expect(idle).toMatchObject({ invite: true, button: 'Показать обмен' })
    expect(trafficView(null, { busy: true }).button).toBe('Считаем…')
    const known = trafficView({ known: true, rx: '3,0 ГБ', tx: '1,0 МБ', points: 24, empty: false })
    expect(known).toMatchObject({ invite: false, rx: '3,0 ГБ', tx: '1,0 МБ', note: 'роутер посчитал сам', button: 'Пересчитать' })
    expect(JSON.stringify(known)).not.toContain('точек')
    const failed = trafficView(null, { error: 'Не получилось.' })
    expect(failed).toMatchObject({ invite: true, error: true, text: 'Не получилось.', button: 'Повторить' })
    expect(trafficView(null, { error: 'Не получилось.', busy: true })).toMatchObject({ button: 'Считаем…' })
    expect(trafficView(null, { error: 'x', busy: true }).error).toBeUndefined()
  })
})

// Сырой ответ агента в JSX («{x.output || x.status}») на экран не выходит.
describe('исходники: ни «output || status» в разметке', () => {
  it('ни один экран не печатает result.output/status напрямую', () => {
    const dir = new URL('../src/screens/', import.meta.url)
    const bad = []
    for (const f of readdirSync(dir)) {
      if (!f.endsWith('.jsx')) continue
      const src = readFileSync(new URL(f, dir), 'utf8')
      for (const [i, line] of src.split('\n').entries()) {
        if (/\{[^}]*\.output\s*\|\|[^}]*\}/.test(line) && /state-error|<p|<span|<div/.test(line)) bad.push(`${f}:${i + 1}`)
        // Шаблонная строка: `…${res.output || res.status}` и `…${x.status}`.
        if (/`[^`]*\$\{[^}]*\.output\s*\|\|[^}]*\}/.test(line)) bad.push(`${f}:${i + 1}`)
        if (/`[^`]*\$\{[^{}]*(res|result)\??\.status\}/.test(line)) bad.push(`${f}:${i + 1}`)
        if (/\{[^{}]*\.status\}/.test(line) && /result|res\./.test(line) && /<p|<span|<div/.test(line)) bad.push(`${f}:${i + 1}`)
      }
    }
    expect(bad).toEqual([])
  })
})
