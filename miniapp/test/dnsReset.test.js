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
    { check_name: 'resolver_guard', status: 'ok', ts: '2026-09-14T10:00:00Z', details: {} },
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
      { check_name: 'resolver_guard', status: 'ok', ts: '2026-09-14T10:05:00Z', details: {} },
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
      { check_name: 'resolver_guard', status: 'ok', ts: '2026-09-14T10:05:00Z', details: { idle: true } },
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

  // После Fix 2 остановленный сторож просто перестаёт присылать строку -- та
  // же «выключен», а не «пропал», хотя ДО сброса строка была.
  it('строка была до сброса и пропала после -- тоже «сторож выключен», а не поломка', () => {
    const after = [{ check_name: 'dns_split', ts: '2026-09-14T10:05:00Z', details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'yandex_dot' }, resolves: 'ok' } }]
    const guard = postconditionRows({ before, after }).find((r) => r.key === 'guard')
    expect(guard).toEqual({ key: 'guard', title: 'Свой DNS-сервер остался на месте', value: 'сторож выключен', tone: 'muted' })
  })

  it('сторож на запасных -- «на запасных», не «да»', () => {
    const after = [
      { check_name: 'dns_split', ts: '2026-09-14T10:05:00Z', details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'yandex_dot' }, resolves: 'ok' } },
      { check_name: 'resolver_guard', status: 'fail', ts: '2026-09-14T10:05:00Z', details: { mode: 'fallback', reason: 'fallback' } },
    ]
    const guard = postconditionRows({ before, after }).find((r) => r.key === 'guard')
    expect(guard).toEqual({ key: 'guard', title: 'Свой DNS-сервер остался на месте', value: 'на запасных', tone: 'warn' })
  })

  it('сторож ещё не прочитал настройки -- отдельная строка, не «да»', () => {
    const after = [
      { check_name: 'dns_split', ts: '2026-09-14T10:05:00Z', details: { checked_at: '2026-09-14T10:05:00Z', zones: { ru: 'yandex_dot' }, resolves: 'ok' } },
      { check_name: 'resolver_guard', status: 'ok', ts: '2026-09-14T10:05:00Z', details: { ready: false } },
    ]
    const guard = postconditionRows({ before, after }).find((r) => r.key === 'guard')
    expect(guard).toEqual({ key: 'guard', title: 'Свой DNS-сервер остался на месте', value: 'ещё не прочитал настройки', tone: 'muted' })
  })

  it('без жаргона', () => {
    const text = JSON.stringify(dnsResetScreenTexts())
    for (const w of ['DoH', 'DoT', 'апстрим', 'dry_run', 'resolver_guard', 'dns_split']) expect(text, w).not.toContain(w)
  })
})

describe('v0.57: проба эталона перед применением', () => {
  const PROBES = [
    { server: '9.9.9.9', purpose: 'foreign', ok: true },
    { server: '1.1.1.1', purpose: 'foreign', ok: false, error: 'нет ответа за 5s' },
    { server: 'common.dot.dns.yandex.net', purpose: 'ru', ok: true },
  ]
  it('пробы -- из payload; старый агент без payload -- null', async () => {
    const { parseProbes } = await import('../src/dnsReset.js')
    expect(parseProbes({ status: 'ok', output: 'x', payload: { probes: PROBES } })).toEqual([
      { server: '9.9.9.9', purpose: 'foreign', ok: true, error: '' },
      { server: '1.1.1.1', purpose: 'foreign', ok: false, error: 'нет ответа за 5s' },
      { server: 'common.dot.dns.yandex.net', purpose: 'ru', ok: true, error: '' },
    ])
    expect(parseProbes({ status: 'ok', output: 'x', payload: JSON.stringify({ probes: PROBES }) })).toHaveLength(3)
    expect(parseProbes({ status: 'ok', output: 'x' })).toBeNull()
    expect(parseProbes({ status: 'ok', payload: { probes: [] } })).toBeNull()
    expect(parseProbes({ status: 'ok', payload: { probes: [{ ok: true }] } })).toBeNull()
    expect(parseProbes(null)).toBeNull()
  })
  it('строки проб: сервер, назначение, отвечает / не отвечает', async () => {
    const { parseProbes, probeRows } = await import('../src/dnsReset.js')
    const rows = probeRows(parseProbes({ payload: { probes: PROBES } }))
    expect(rows).toEqual([
      { key: '9.9.9.9', title: '9.9.9.9 · заграничный', value: 'отвечает', tone: 'ok' },
      { key: '1.1.1.1', title: '1.1.1.1 · заграничный', value: 'не отвечает', tone: 'danger', detail: 'нет ответа за 5s' },
      { key: 'common.dot.dns.yandex.net', title: 'common.dot.dns.yandex.net · Яндекс, русские зоны', value: 'отвечает', tone: 'ok' },
    ])
  })
  it('пропущенные серверы -- словами; Яндекс не отвечает -- куда уйдут русские зоны', async () => {
    const { skippedServersText } = await import('../src/dnsReset.js')
    expect(skippedServersText(PROBES)).toBe('Не отвечает и в роутер не ставится: 1.1.1.1.')
    expect(skippedServersText([{ server: 'common.dot.dns.yandex.net', purpose: 'ru', ok: false, error: 'x' }, { server: '9.9.9.9', purpose: 'foreign', ok: true }])).toBe(
      'Не отвечает и в роутер не ставится: common.dot.dns.yandex.net. Русские зоны пойдут через заграничные серверы — это работает.',
    )
    expect(skippedServersText([{ server: 'a', purpose: 'foreign', ok: false }, { server: 'b', purpose: 'foreign', ok: false }])).toBe('Не отвечают и в роутер не ставятся: a, b.')
    expect(skippedServersText(PROBES.filter((p) => p.ok))).toBe('')
    expect(skippedServersText(null)).toBe('')
  })
  it('reference_unreachable -- человеческим текстом', async () => {
    const { referenceUnreachable, dnsResetScreenTexts } = await import('../src/dnsReset.js')
    const res = { status: 'err', output: 'reference_unreachable: не ответил ни один заграничный DNS-сервер эталона — сброс не применён' }
    expect(referenceUnreachable(res)).toBe(true)
    expect(referenceUnreachable({ status: 'err', output: 'read config: boom' })).toBe(false)
    expect(referenceUnreachable({ status: 'ok', output: 'reference_unreachable' })).toBe(false)
    const t = dnsResetScreenTexts()
    expect(t.unreachable).toContain('Ни один заграничный DNS-сервер эталона не ответил')
    expect(t.unreachable).toContain('ничего не изменено')
    expect(t.unreachable).toContain('нет интернета')
    expect(t.noProbes).toBe('Проверка доступности — с агента v0.57.')
    expect(t.title).toBe('Эталонный DNS')
  })
  it('итог сброса при reference_unreachable -- не «не смог прочитать настройки»', async () => {
    const { parseReset, doneText, dnsResetScreenTexts } = await import('../src/dnsReset.js')
    const r = parseReset({ status: 'err', output: 'reference_unreachable: …' })
    expect(r.unreachable).toBe(true)
    expect(doneText(r)).toBe(dnsResetScreenTexts().unreachable)
  })
  it('предпросмотр нового агента с разделом проб разбирается как прежде', () => {
    const out = [
      'Предпросмотр сброса DNS — ничего не изменено',
      '',
      'проверка доступности эталона (3):',
      '  ✓ 9.9.9.9 — отвечает',
      '  ✗ 1.1.1.1 — не отвечает: нет ответа за 5s',
      '  ✓ common.dot.dns.yandex.net — отвечает',
      'сервер не отвечает, не ставим (1):',
      '  · tls upstream 1.1.1.1 sni cloudflare-dns.com',
      '',
      'Уберём (1):',
      '  − tls upstream 8.8.8.8 sni dns.google',
      '',
      'Заменим на эталонные (7):',
      '  + tls upstream 9.9.9.9 sni dns.quad9.net',
    ].join('\n')
    expect(parsePreview(out)).toEqual({ remove: ['tls upstream 8.8.8.8 sni dns.google'], keep: [], addCount: 7, skippedCount: 0 })
  })
})
