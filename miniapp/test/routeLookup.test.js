import { describe, it, expect } from 'vitest'
import { normalizeSiteInput, looksLikeSite, lookupAnswer, lookupRefusal } from '../src/routeLookup.js'
import { commandOutcomeLabel } from '../src/labels.js'

const NOT_A_SITE = 'Это не похоже на адрес сайта — нужно имя вроде claude.ai'

// Та же проверка, что у сервера (sanitizeWizardCommandArgs, route_lookup):
// то, что сервер отобьёт, до него не отправляется, а человек сразу слышит,
// что не так с тем, что он ввёл.
describe('looksLikeSite', () => {
  it('имя сайта с точкой -- да', () => {
    expect(looksLikeSite('claude.ai')).toBe(true)
    expect(looksLikeSite('www.example.com')).toBe(true)
  })

  it('пустое, без точки, с пробелом, двоеточием, путём или длиннее 253 -- нет', () => {
    for (const bad of ['', 'localhost', 'a b.com', 'x.com:abc', 'x.com/path', 'a'.repeat(250) + '.com']) {
      expect(looksLikeSite(bad), bad).toBe(false)
    }
  })
})

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
  'exit_unrecognized:Guest network':
    'Сайт уйдёт через подключение «Guest network» — роутер не сказал, VPN-туннель это или провайдер',
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

  // «Неизвестно» -- это ответ, догадка -- нет. Если роутер не раскрыл список
  // (geosite:ANTHROPIC на панели без раскрытия) или правило записано
  // шаблоном, «правил нет» -- лишь «не нашлось среди проверенного»: сайт
  // может сидеть как раз в нераскрытом списке. Уверенный зелёный ответ здесь
  // был бы уверенно неверным.
  const HEDGE = 'не нашлось, но часть правил проверить не удалось — сайт, скорее всего, пойдёт'

  it('правил не нашлось, но список не раскрыт -- ответ с оговоркой, не уверенный', () => {
    const a = lookupAnswer(
      answer({ verdict: 'direct', tunnel_id: '', tunnel_name: '', by_default: true, matches: [], notes: ['geo_expand_failed:ANTHROPIC'] }),
    )
    expect(a.title).toBe(`Правил для «claude.ai» ${HEDGE} напрямую через провайдера`)
    expect(a.lines).toContain(NOTE_WORDS['geo_expand_failed:ANTHROPIC'])
    expect(a.tone).toBe('warn')
  })

  it('правил не нашлось, но часть правил -- шаблоны: тоже оговорка, и главный выход назван', () => {
    const a = lookupAnswer(answer({ by_default: true, matches: [], notes: ['regexp_unchecked'] }))
    expect(a.title).toBe(`Правил для «claude.ai» ${HEDGE} главным выходом роутера, через VPN-туннель «vpn-nl»`)
    expect(a.lines).toContain(NOTE_WORDS.regexp_unchecked)
    expect(a.tone).toBe('warn')
  })

  it('без таких примечаний «правил нет» остаётся уверенным', () => {
    for (const notes of [undefined, [], ['ip_rules_unchecked']]) {
      const a = lookupAnswer(answer({ verdict: 'direct', tunnel_id: '', tunnel_name: '', by_default: true, matches: [], notes }))
      expect(a.title).toBe('Правил для «claude.ai» нет — сайт пойдёт напрямую через провайдера')
      expect(a.tone).toBe('ok')
    }
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
  // Сервер отбивает имя кодом 400 invalid_domain, а текст ошибки у api.js --
  // «<путь> failed: 400», без кода. Код приходит отдельно (useCommand
  // errorCode), и именно по нему человек обязан услышать, что не так с
  // введённым, а не «роутер не ответил».
  it('отказ сервера 400 invalid_domain -- про адрес сайта, а не про роутер', () => {
    expect(lookupRefusal('/routers/7/commands failed: 400', 'invalid_domain')).toBe(NOT_A_SITE)
  })

  it('отказ агента по имени -- то же самое', () => {
    expect(lookupRefusal('route_lookup: invalid domain')).toBe(NOT_A_SITE)
  })

  // Агент v0.29 route_lookup не знает и отвечает «unknown action:
  // route_lookup». «Попробуйте ещё раз» здесь -- совет повторять вечно: дело
  // в версии, и чинится оно обновлением агента. Слова -- те же, что у любой
  // другой команды на старом агенте (labels.js, commandOutcomeLabel).
  it('старый агент не знает вопроса -- «обновите агента», а не «попробуйте ещё раз»', () => {
    const old = commandOutcomeLabel('route_lookup', { status: 'err', output: 'unknown action: route_lookup' })
    expect(old).toBe('Агент на этом роутере старше приложения и такого пока не умеет — обновите агента.')
    expect(lookupRefusal('unknown action: route_lookup')).toBe(old)
    expect(lookupRefusal('Unknown action: route_lookup', 'unknown')).toBe(old)
  })

  it('прочие отказы -- «роутер не ответил»', () => {
    expect(lookupRefusal('awgmgr GET /api/dns-routes/list: HTTP 500')).toBe('Роутер не ответил на вопрос — попробуйте ещё раз')
    expect(lookupRefusal('/routers/7/commands failed: 502', 'unknown')).toBe('Роутер не ответил на вопрос — попробуйте ещё раз')
  })
})
