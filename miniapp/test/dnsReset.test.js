import { describe, it, expect } from 'vitest'
import {
  dnsResetAvailable,
  resetEnabled,
  parsePreview,
  previewText,
  parseReset,
  doneText,
  postconditionRows,
  dnsResetConfirmBody,
  dnsResetScreenTexts,
} from '../src/dnsReset.js'
import { commandOutcomeLabel } from '../src/labels.js'

// Ответы агента дословно (internal/agent/actions/dns_reset.go).
const PREVIEW = `Предпросмотр сброса DNS — ничего не изменено

Уберём (3):
  − tls upstream 8.8.8.8 sni dns.google
  − https upstream https://dns.google/dns-query
  − tls upstream 1.1.1.1 sni cloudflare-dns.com

Оставим как есть (1):
  = https upstream https://resolver.example.com/***

Заменим на эталонные (9):
  + tls upstream 9.9.9.9 sni dns.quad9.net
  + tls upstream 1.1.1.1 sni cloudflare-dns.com
  + tls upstream common.dot.dns.yandex.net domain ru
`

const RESET_OK = `DNS reset → reference DoT

снимок «до»: /opt/etc/wg-monitor/dns-before-1757600000.txt

remove existing dns-proxy upstreams:
  ok  dns-proxy no tls upstream 8.8.8.8
`

describe('кому экран доступен', () => {
  // Преграда 2 (экран): старому агенту экран не рисуется вовсе -- он не знает
  // dry_run и на «посмотреть» сделал бы настоящий сброс.
  it('роутеру со старым агентом экран не показывается', () => {
    expect(dnsResetAvailable({ role: 'admin', agent_version: 'v0.30.1' })).toBe(false)
    expect(dnsResetAvailable({ role: 'admin', agent_version: '' })).toBe(false)
    expect(dnsResetAvailable({ role: 'admin', agent_version: 'v0.31.0-rc1' })).toBe(false)
    expect(dnsResetAvailable({ role: 'admin', agent_version: 'v0.31.0' })).toBe(true)
  })

  it('не админу -- нет, даже с новым агентом', () => {
    expect(dnsResetAvailable({ role: 'owner', agent_version: 'v0.31.0' })).toBe(false)
    expect(dnsResetAvailable({ role: 'operator', agent_version: 'v0.31.0' })).toBe(false)
    expect(dnsResetAvailable(null)).toBe(false)
  })
})

describe('предпросмотр', () => {
  it('кнопка настоящего сброса не активна, пока не отработал предпросмотр', () => {
    expect(resetEnabled({ previewed: false })).toBe(false)
    expect(resetEnabled({})).toBe(false)
    expect(resetEnabled({ previewed: true })).toBe(true)
  })

  it('разбирает ответ агента на три списка', () => {
    const p = parsePreview(PREVIEW)
    expect(p.remove).toHaveLength(3)
    expect(p.keep).toEqual(['https upstream https://resolver.example.com/***'])
    expect(p.addCount).toBe(9)
  })

  it('не предпросмотр -- не разбирается: настоящий сброс за предпросмотр не выдаётся', () => {
    expect(parsePreview(RESET_OK)).toBeNull()
    expect(parsePreview('')).toBeNull()
  })

  it('словами: сколько строк сейчас, сколько поставим, свой DNS-сервер не тронем', () => {
    expect(previewText(parsePreview(PREVIEW))).toBe(
      'Сейчас на роутере 4 строки DNS. Заменим на эталонные: 9. Свой DNS-сервер не тронем.',
    )
    expect(previewText({ remove: ['a', 'b', 'c', 'd', 'e'], keep: [], addCount: 9 })).toBe(
      'Сейчас на роутере 5 строк DNS. Заменим на эталонные: 9.',
    )
    expect(previewText({ remove: ['a'], keep: [], addCount: 9 })).toBe('Сейчас на роутере 1 строка DNS. Заменим на эталонные: 9.')
  })

  it('подтверждение называет роутер и последствие', () => {
    expect(dnsResetConfirmBody('home-keen')).toBe(
      'Наберите имя роутера «home-keen», чтобы подтвердить сброс DNS. Роутер заменит свои DNS-серверы эталонными и сохранит настройки.',
    )
  })
})

describe('после сброса', () => {
  it('экран называет путь к файлу, а не обещает «мы сохранили»', () => {
    const text = doneText(parseReset({ status: 'ok', output: RESET_OK }))
    expect(text).toBe(
      'Готово. Прежние настройки DNS сохранены на роутере в файле /opt/etc/wg-monitor/dns-before-1757600000.txt — по нему их можно вернуть руками.',
    )
  })

  it('снимок не записан -- так и сказано', () => {
    const r = parseReset({ status: 'ok', output: 'DNS reset → reference DoT\n\nснимок «до» не записан: read-only file system\n' })
    expect(doneText(r)).toBe('Готово, но прежние настройки сохранить не удалось — вернуть их будет не по чему.')
  })

  it('частичный сброс -- не «готово»', () => {
    const r = parseReset({ status: 'partial', output: RESET_OK })
    expect(doneText(r)).toBe(
      'Сброс прошёл не целиком: часть команд роутер не принял. Прежние настройки DNS сохранены на роутере в файле /opt/etc/wg-monitor/dns-before-1757600000.txt — по нему их можно вернуть руками.',
    )
    expect(commandOutcomeLabel('dns_reset', { status: 'partial', output: RESET_OK })).toBe('Сброс DNS прошёл не целиком — подробности на экране')
  })

  it('ошибка -- роутер ничего не менял', () => {
    expect(doneText(parseReset({ status: 'err', output: 'read running-config failed' }))).toBe(
      'Роутер не сбросил DNS: не смог прочитать свои настройки. Ничего не изменилось.',
    )
  })

  it('обещания «вернём одним нажатием» на экране нет', () => {
    expect(JSON.stringify(dnsResetScreenTexts())).not.toMatch(/одним нажатием|откатить автоматически|вернём/)
  })
})

describe('три постусловия', () => {
  const before = [
    { check_name: 'dns_split', ts: '2026-09-14T10:00:00Z', details: { checked_at: '2026-09-14T10:00:00Z', zones: { ru: 'other' }, resolves: 'ok' } },
    { check_name: 'resolver_guard', ts: '2026-09-14T10:00:00Z', details: {} },
  ]

  it('пока роутер не прислал новый отчёт -- ждём, а не «да»', () => {
    const rows = postconditionRows({ before, after: before })
    expect(rows.map((r) => r.value)).toEqual(['ждём отчёта', 'ждём отчёта', 'ждём отчёта'])
  })

  it('свежий отчёт: имена резолвятся, сторож на месте, зоны у Яндекса', () => {
    const after = [
      {
        check_name: 'dns_split',
        ts: '2026-09-14T10:05:00Z',
        details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'yandex_dot', su: 'yandex_dot' }, resolves: 'ok' },
      },
      { check_name: 'resolver_guard', ts: '2026-09-14T10:05:00Z', details: {} },
    ]
    expect(postconditionRows({ before, after })).toEqual([
      { key: 'resolves', title: 'Роутер отвечает на запросы имён сайтов', value: 'да', tone: 'ok' },
      { key: 'guard', title: 'Свой DNS-сервер остался на месте', value: 'да', tone: 'ok' },
      { key: 'zones', title: 'Русские зоны у Яндекса', value: '2 из 2', tone: 'ok' },
    ])
  })

  it('плохие исходы названы, а не спрятаны', () => {
    const after = [
      {
        check_name: 'dns_split',
        ts: '2026-09-14T10:05:00Z',
        details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'unknown', su: 'unknown' }, resolves: 'fail' },
      },
      { check_name: 'resolver_guard', ts: '2026-09-14T10:05:00Z', details: { idle: true } },
    ]
    expect(postconditionRows({ before, after }).map((r) => [r.value, r.tone])).toEqual([
      ['нет', 'danger'],
      ['нет — сторож его потерял', 'danger'],
      ['неизвестно по всем зонам', 'warn'],
    ])
  })

  it('сторож выключен на роутере -- так и сказано, это не поломка', () => {
    const after = [{ check_name: 'dns_split', ts: '2026-09-14T10:05:00Z', details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'yandex_dot' }, resolves: 'ok' } }]
    const guard = postconditionRows({ before: [before[0]], after }).find((r) => r.key === 'guard')
    expect(guard).toEqual({ key: 'guard', title: 'Свой DNS-сервер остался на месте', value: 'сторож выключен', tone: 'muted' })
  })

  it('без жаргона', () => {
    const text = JSON.stringify(dnsResetScreenTexts())
    for (const w of ['DoH', 'DoT', 'апстрим', 'dry_run', 'resolver_guard', 'dns_split']) expect(text, w).not.toContain(w)
  })
})
