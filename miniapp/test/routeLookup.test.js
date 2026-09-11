import { describe, it, expect } from 'vitest'
import { normalizeSiteInput, lookupAnswer, lookupRefusal } from '../src/routeLookup.js'

describe('normalizeSiteInput', () => {
  it('оставляет от ссылки одно имя сайта', () => {
    expect(normalizeSiteInput('https://Claude.ai/new?x=1')).toBe('claude.ai')
    expect(normalizeSiteInput('http://example.com#top')).toBe('example.com')
    expect(normalizeSiteInput('  Example.COM.  ')).toBe('example.com')
  })

  it('снимает порт и путь, но не трогает www.', () => {
    expect(normalizeSiteInput('www.example.com:8443/')).toBe('www.example.com')
  })

  it('пустое остаётся пустым', () => {
    expect(normalizeSiteInput('  ')).toBe('')
    expect(normalizeSiteInput(undefined)).toBe('')
  })
})

const rule = (over = {}) => ({
  rule_name: 'Все AI сервисы',
  pattern: 'geosite:ANTHROPIC',
  via: 'tunnel',
  tunnel_id: 'awg1',
  tunnel_name: 'vpn-nl',
  ...over,
})

const answer = (over = {}) => ({
  domain: 'claude.ai',
  verdict: 'tunnel',
  tunnel_id: 'awg1',
  tunnel_name: 'vpn-nl',
  by_default: false,
  matches: [rule()],
  ...over,
})

const NOTE_WORDS = {
  hr_not_running: 'HydraRoute Neo — движок умной раздельной маршрутизации — сейчас не запущен, его правила не действуют',
  ip_rules_unchecked: 'Есть правила по адресу — по имени сайта их не проверить',
  regexp_unchecked: 'Часть правил записана шаблоном — их не проверить',
  'geo_expand_failed:ANTHROPIC': 'Роутер не раскрыл список «ANTHROPIC»',
  policies_unknown: 'Роутер не отдал общие наборы правил',
  singbox_router: 'Трафиком управляет sing-box — он решает сам',
}

describe('lookupAnswer', () => {
  it('туннель по правилу: куда и каким правилом', () => {
    const a = lookupAnswer(answer())
    expect(a.title).toBe('«claude.ai» пойдёт через VPN-туннель «vpn-nl»')
    expect(a.lines).toContain('правило «Все AI сервисы»')
    expect(a.tone).toBe('ok')
  })

  it('принимает и JSON-строку -- так приходит output команды', () => {
    expect(lookupAnswer(JSON.stringify(answer())).title).toBe('«claude.ai» пойдёт через VPN-туннель «vpn-nl»')
  })

  it('правил нет, главный выход -- провайдер', () => {
    const a = lookupAnswer(answer({ verdict: 'direct', tunnel_id: '', tunnel_name: '', by_default: true, matches: [] }))
    expect(a.title).toBe('Правил для «claude.ai» нет — сайт пойдёт напрямую через провайдера')
    expect(a.tone).toBe('ok')
  })

  it('правил нет, главный выход -- VPN-туннель: сайт идёт через него, а не напрямую', () => {
    const a = lookupAnswer(answer({ domain: 'x.example.com', by_default: true, matches: [] }))
    expect(a.title).toBe('Правил для «x.example.com» нет — сайт пойдёт главным выходом роутера, через VPN-туннель «vpn-nl»')
  })

  it('напрямую по правилу', () => {
    const a = lookupAnswer(
      answer({ domain: 'bank.example.com', verdict: 'direct', tunnel_id: '', tunnel_name: '', matches: [rule({ rule_name: 'Банк', via: 'direct', tunnel_id: '', tunnel_name: '' })] }),
    )
    expect(a.title).toBe('«bank.example.com» пойдёт напрямую через провайдера')
    expect(a.lines).toContain('правило «Банк»')
  })

  it('правила ведут в разные места: по строке на правило', () => {
    const a = lookupAnswer(
      answer({
        domain: 'chat.example.com',
        verdict: 'mixed',
        tunnel_id: '',
        tunnel_name: '',
        matches: [
          rule({ rule_name: 'Первое' }),
          rule({ rule_name: 'Второе', tunnel_id: 'awg2', tunnel_name: 'vpn-reserve' }),
          rule({ rule_name: 'Третье', via: 'direct', tunnel_id: '', tunnel_name: '' }),
        ],
      }),
    )
    expect(a.title).toBe('Несколько правил ведут «chat.example.com» в разные места')
    expect(a.lines).toEqual([
      'правило «Первое» — через VPN-туннель «vpn-nl»',
      'правило «Второе» — через VPN-туннель «vpn-reserve»',
      'правило «Третье» — напрямую через провайдера',
    ])
    expect(a.tone).toBe('warn')
  })

  it('неизвестно: говорит, почему', () => {
    const a = lookupAnswer(answer({ verdict: 'unknown', tunnel_id: '', tunnel_name: '', matches: [], notes: ['singbox_router'] }))
    expect(a.title).toBe('Не удалось узнать, куда пойдёт «claude.ai»')
    expect(a.lines).toContain(NOTE_WORDS.singbox_router)
    expect(a.tone).toBe('unknown')
  })

  it('неизвестный главный выход без пояснений от роутера всё равно объяснён', () => {
    const a = lookupAnswer(answer({ verdict: 'unknown', tunnel_id: '', tunnel_name: '', by_default: true, matches: [] }))
    expect(a.title).toBe('Не удалось узнать, куда пойдёт «claude.ai»')
    expect(a.lines.length).toBeGreaterThan(0)
  })

  for (const [code, words] of Object.entries(NOTE_WORDS)) {
    it(`примечание ${code} становится словами`, () => {
      const a = lookupAnswer(answer({ verdict: 'direct', tunnel_id: '', tunnel_name: '', by_default: true, matches: [], notes: [code] }))
      expect(a.lines).toContain(words)
      expect(a.lines.join(' ')).not.toContain(code)
    })
  }

  it('остановленный HydraRoute Neo -- повод насторожиться', () => {
    const a = lookupAnswer(answer({ verdict: 'direct', tunnel_id: '', tunnel_name: '', by_default: true, matches: [], notes: ['hr_not_running'] }))
    expect(a.tone).toBe('warn')
  })

  it('непонятный ответ -- «неизвестно», а не падение', () => {
    expect(lookupAnswer(null).tone).toBe('unknown')
    expect(lookupAnswer('не json').tone).toBe('unknown')
  })

  it('ни в одном тексте нет слов «политика», «geosite», «DoH», «direct»', () => {
    const all = [
      answer(),
      answer({ by_default: true, matches: [] }),
      answer({ verdict: 'direct', by_default: true, matches: [] }),
      answer({ verdict: 'direct', matches: [rule({ via: 'direct' })] }),
      answer({ verdict: 'mixed', matches: [rule(), rule({ via: 'direct' }), rule({ via: 'unknown' })] }),
      answer({ verdict: 'unknown', matches: [rule({ via: 'unknown' })], notes: Object.keys(NOTE_WORDS) }),
      answer({ verdict: 'unknown', by_default: true, matches: [] }),
      null,
    ]
    for (const r of all) {
      const a = lookupAnswer(r)
      const text = [a.title, ...a.lines].join('\n')
      for (const banned of [/политик/i, /geosite/i, /\bDoH\b/i, /direct/i]) {
        expect(text).not.toMatch(banned)
      }
    }
  })
})

describe('lookupRefusal', () => {
  it('отказ агента по имени -- «не похоже на имя сайта», по-русски', () => {
    expect(lookupRefusal('route_lookup: invalid domain')).toBe('Это не похоже на имя сайта')
    expect(lookupRefusal('awgmgr GET /api/dns-routes/list: HTTP 500')).toBe('Роутер не ответил на вопрос — попробуйте ещё раз')
  })
})
