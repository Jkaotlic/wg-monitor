import { describe, it, expect } from 'vitest'
import { templateGroups, addPlanSummary, deletePlanSummary, parseManualTargets, templateChoice, skippedNote, manualPlanArgs, geoSearch, geoTarget, geoRuleName, geoCountLabel, migratePlanSummary, canMigrateRule } from '../src/routeAdd.js'

describe('templateGroups', () => {
  // На живом роутере 87 наборов в семи категориях. Плоским списком по нему
  // невозможно попасть пальцем, а сортировка по частоте была бы догадкой --
  // имя же факт.
  it('группирует по категориям и сортирует их по имени', () => {
    const groups = templateGroups([
      { id: 'a', name: 'OpenAI', category: 'ai' },
      { id: 'b', name: 'Netflix', category: 'media' },
      { id: 'c', name: 'Claude', category: 'ai' },
    ])
    expect(groups.map((g) => g.category)).toEqual(['ai', 'media'])
    expect(groups[0].items.map((i) => i.name)).toEqual(['Claude', 'OpenAI'])
  })

  it('набор без категории не теряется', () => {
    const groups = templateGroups([{ id: 'x', name: 'Своё' }])
    expect(groups).toHaveLength(1)
    expect(groups[0].items[0].name).toBe('Своё')
  })

  it('пустой каталог даёт пустой список', () => {
    expect(templateGroups(null)).toEqual([])
  })
})

describe('addPlanSummary', () => {
  it('называет цели целиком и блокирует применение при жёстком пересечении', () => {
    const s = addPlanSummary({
      route: { name: 'ChatGPT', targets: [{ value: 'openai.com' }, { value: 'chatgpt.com' }] },
      overlaps: [{ severity: 'block', reason: 'уже есть правило на openai.com' }],
      can_apply: false,
    })
    expect(s.canApply).toBe(false)
    expect(s.lines.join(' ')).toContain('openai.com')
    expect(s.lines.join(' ')).toContain('уже есть правило')
  })

  it('без пересечений применение разрешено', () => {
    const s = addPlanSummary({ route: { name: 'GITHUB', targets: [{ value: 'github.com' }] }, can_apply: true })
    expect(s.canApply).toBe(true)
    expect(s.title).toBe('GITHUB')
  })

  // Хеш черновика едет обратно вместе с подтверждением: агент откажется
  // применять план, чьё превью устарело, и без хеша эта защита не работает.
  // Цели -- это код (мono), а пересечение -- фраза человеку. Одной кучей
  // строк экран красит их одинаково, и предупреждение читается как ещё один
  // домен.
  it('цели и предупреждения разделены', () => {
    const s = addPlanSummary({
      route: { name: 'ChatGPT', targets: [{ value: 'openai.com' }] },
      overlaps: [{ severity: 'block', reason: 'уже есть правило' }],
    })
    expect(s.targets).toBe('openai.com')
    expect(s.notes).toEqual(['Мешает: уже есть правило'])
    expect(s.lines).toEqual(['openai.com', 'Мешает: уже есть правило'])
  })

  it('несёт хеш плана для подтверждения', () => {
    const s = addPlanSummary({ route: { name: 'X' }, hash: 'abc123', can_apply: true })
    expect(s.hash).toBe('abc123')
  })
})

describe('deletePlanSummary', () => {
  // Удаление необратимо в один клик, поэтому цели называются целиком:
  // "и ещё 4" -- это скрытая часть последствия.
  it('перечисляет все цели удаляемого правила', () => {
    const s = deletePlanSummary({
      route: { name: 'ChatGPT', targets: [{ value: 'openai.com' }, { value: 'chatgpt.com' }, { value: 'oaistatic.com' }] },
      can_apply: true,
    })
    expect(s.lines[0]).toBe('openai.com, chatgpt.com, oaistatic.com')
    expect(s.lines[0]).not.toContain('и ещё')
  })

  it('предупреждения плана попадают в превью', () => {
    const s = deletePlanSummary({
      route: { name: 'geoip:ru', targets: [] },
      warnings: [{ severity: 'warn', reason: 'последнее правило политики RU' }],
      can_apply: true,
    })
    expect(s.lines.join(' ')).toContain('последнее правило политики RU')
  })
})

describe('parseManualTargets', () => {
  // Правило либо про имена сайтов, либо про адреса сетей: агент проверяет
  // это первым делом (buildRouteAddPlan, route_add_delete.go), и узнавать
  // об ошибке после похода на роутер -- лишний круг.
  it('домены дают правило по имени сайта', () => {
    const p = parseManualTargets(' openai.com, chatgpt.com \n anthropic.com ')
    expect(p.kind).toBe('dns')
    expect(p.targets).toEqual(['openai.com', 'chatgpt.com', 'anthropic.com'])
    expect(p.error).toBe('')
  })

  it('сети и адреса дают правило по адресу сети', () => {
    const p = parseManualTargets('10.0.0.0/8 192.168.5.7')
    expect(p.kind).toBe('static')
    expect(p.targets).toEqual(['10.0.0.0/8', '192.168.5.7'])
  })

  it('смешивать имена и адреса в одном правиле нельзя', () => {
    const p = parseManualTargets('openai.com, 10.0.0.0/8')
    expect(p.kind).toBe('')
    expect(p.error).toContain('в одном правиле')
  })

  it('пустой ввод -- не ошибка, а пустой список', () => {
    expect(parseManualTargets('  ')).toEqual({ targets: [], kind: '', error: '' })
  })

  it('повторы схлопываются: роутер всё равно хранит их один раз', () => {
    expect(parseManualTargets('openai.com openai.com').targets).toEqual(['openai.com'])
  })
})

describe('templateChoice', () => {
  const AI = { id: 'ai', name: 'AI', category: 'ai', dns: ['openai.com'], hr_neo: ['geosite:OPENAI', 'geoip:AI'] }
  const GEO = { id: 'geo', name: 'Гео', category: 'ai', hr_neo: ['geosite:GITHUB'] }

  // Гео-теги умеет разворачивать только HR Neo. Набор из одних тегов на
  // роутере без него -- правило без целей: агент ответит ошибкой, и лучше
  // сказать это на экране, чем сходить за ней на роутер.
  it('с работающим HR Neo гео-теги идут в правило', () => {
    const c = templateChoice(AI, { hrNeoRunning: true })
    expect(c.args).toEqual({ template_id: 'ai', kind: 'dns', use_hr_neo: true })
    expect(c.canApply).toBe(true)
    expect(c.summary).toBe('1 домен и 2 гео-тега')
  })

  it('без HR Neo остаются только домены', () => {
    const c = templateChoice(AI, { hrNeoRunning: false })
    expect(c.args.use_hr_neo).toBe(false)
    expect(c.canApply).toBe(true)
    expect(c.summary).toBe('1 домен')
  })

  // Жалоба оператора 27.10.2026: с awg-manager 2.17.2 в наборах нет гео-тегов,
  // и флаг, завязанный на них, молча слал всё в NDMS. Движок -- свойство
  // роутера: работает HR Neo -- правило идёт в него, что бы ни нёс набор.
  it('набор из одних доменов при работающем HR Neo идёт в HR Neo', () => {
    const c = templateChoice({ id: 'yt', name: 'YouTube', dns: ['youtube.com'] }, { hrNeoRunning: true })
    expect(c.args).toEqual({ template_id: 'yt', kind: 'dns', use_hr_neo: true })
    expect(c.summary).toBe('1 домен')
  })

  it('набор из одних гео-тегов без HR Neo применить нельзя, и экран говорит почему', () => {
    const c = templateChoice(GEO, { hrNeoRunning: false })
    expect(c.canApply).toBe(false)
    expect(c.reason).toContain('HydraRoute Neo')
    // Имя движка человеку ничего не говорит -- рядом обязано стоять, что он делает.
    expect(c.reason).toContain('движок умной раздельной маршрутизации')
    expect(c.reason).not.toMatch(/HR Neo/)
  })
})

// Живой роутер (awg-manager 2.17.2+r21) описывает часть наборов правилами
// sing-box или ссылкой на подписку -- правилом DNS/HR-Neo их не выразить, и
// агент их не отдаёт. Молча показать 75 из 87 значит соврать размером
// каталога: разница называется словами.
describe('skippedNote', () => {
  it('называет, сколько наборов приложение применить не может', () => {
    expect(skippedNote(12)).toBe('Ещё 12 наборов роутер описывает правилами sing-box — их приложение применить не может.')
  })

  it('один набор -- в единственном числе', () => {
    expect(skippedNote(1)).toContain('Ещё 1 набор ')
  })

  it('нечего пропускать -- нечего и говорить', () => {
    expect(skippedNote(0)).toBe('')
    expect(skippedNote()).toBe('')
  })
})

describe('manualPlanArgs', () => {
  it('сайты при работающем HR Neo идут в HR Neo', () => {
    const args = manualPlanArgs({ parsed: parseManualTargets('figma.com'), tunnelID: 'awg1', name: '', hrNeoRunning: true })
    expect(args).toEqual({ kind: 'dns', tunnel_id: 'awg1', targets: ['figma.com'], name: 'figma.com', use_hr_neo: true })
  })

  it('без HR Neo -- обычное DNS-правило', () => {
    const args = manualPlanArgs({ parsed: parseManualTargets('figma.com'), tunnelID: 'awg1', name: 'Фигма', hrNeoRunning: false })
    expect(args.use_hr_neo).toBe(false)
    expect(args.name).toBe('Фигма')
  })

  it('сети остаются статическим маршрутом и флаг не несут', () => {
    const args = manualPlanArgs({ parsed: parseManualTargets('10.0.0.0/8'), tunnelID: 'awg1', name: '', hrNeoRunning: true })
    expect(args.kind).toBe('static')
    expect(args.use_hr_neo).toBe(false)
  })
})

describe('geoSearch', () => {
  const TAGS = [
    { kind: 'geoip', name: 'YOUTUBE', count: 7 },
    { kind: 'geosite', name: 'GOOGLE', count: 900 },
    { kind: 'geosite', name: 'YOUTUBE', count: 55 },
    { kind: 'geosite', name: 'YOUTUBE-MUSIC', count: 3 },
    { kind: 'geosite', name: 'CATEGORY-YOUTUBE-RU', count: 2 },
  ]

  it('без запроса ничего не показывает -- 1800 строк пальцем не листают', () => {
    expect(geoSearch(TAGS, '  ')).toEqual([])
  })

  it('точное совпадение, затем начало имени, затем вхождение; geosite раньше geoip', () => {
    expect(geoSearch(TAGS, 'youtube').map(geoTarget)).toEqual([
      'geosite:YOUTUBE',
      'geoip:YOUTUBE',
      'geosite:YOUTUBE-MUSIC',
      'geosite:CATEGORY-YOUTUBE-RU',
    ])
  })

  it('понимает запрос с префиксом типа и ограничивает выдачу', () => {
    expect(geoSearch(TAGS, 'geoip:you').map(geoTarget)).toEqual(['geoip:YOUTUBE'])
    expect(geoSearch(TAGS, 'y', { limit: 2 })).toHaveLength(2)
  })

  it('пустой или битый список не роняет поиск', () => {
    expect(geoSearch(null, 'x')).toEqual([])
  })
})

describe('geoRuleName / geoCountLabel', () => {
  it('имя правила -- из имён тегов без повторов', () => {
    expect(geoRuleName(['geosite:YOUTUBE', 'geoip:YOUTUBE', 'geosite:GOOGLE'])).toBe('YOUTUBE, GOOGLE')
    expect(geoRuleName([])).toBe('')
  })

  it('имя не длиннее 64 символов, которые пропускает сервер', () => {
    const many = Array.from({ length: 20 }, (_, i) => `geosite:CATEGORY-NUMBER-${i}`)
    expect(geoRuleName(many).length).toBeLessThanOrEqual(64)
  })

  it('число записей названо словами по типу', () => {
    expect(geoCountLabel({ kind: 'geosite', count: 55 })).toBe('сайты · 55 записей')
    expect(geoCountLabel({ kind: 'geoip', count: 1 })).toBe('сети · 1 запись')
  })
})

// Оператор 28.10.2026: «если включен гидра роут -- все туда, если его нет -- все
// в стандарт с предупреждением». Куда пойдёт правило, решает агент; экран
// читает это из плана (route.backend), а не из своей догадки о роутере.
describe('addPlanSummary: движок правила', () => {
  it('правило HydraRoute Neo названо и не предупреждает', () => {
    const s = addPlanSummary({ route: { name: 'X', backend: 'hydraroute', targets: [{ value: 'a.com' }] }, can_apply: true })
    expect(s.engine).toBe('Через HydraRoute Neo')
    expect(s.engineWarning).toBe('')
  })

  it('без HydraRoute Neo -- стандартная маршрутизация и предупреждение', () => {
    for (const route of [
      { name: 'X', backend: 'ndms', kind: 'dns', targets: [{ value: 'a.com' }] },
      { name: 'Y', kind: 'static', targets: [{ value: '203.0.113.0/24' }] },
    ]) {
      const s = addPlanSummary({ route, can_apply: true })
      expect(s.engine).toBe('Через стандартную маршрутизацию роутера')
      expect(s.engineWarning).toContain('HydraRoute Neo')
      expect(s.engineWarning).toContain('не работает')
    }
  })
})

describe('addPlanSummary: старый агент', () => {
  // Neo на роутере работает, а план пришёл не в него -- значит, агент старый.
  // Писать «Neo не работает» здесь -- неправда; правда -- обновить агента.
  it('при работающем Neo и плане мимо него винит агента, а не роутер', () => {
    const s = addPlanSummary({ route: { name: 'Y', kind: 'static', targets: [{ value: '203.0.113.0/24' }] } }, { hrNeoRunning: true })
    expect(s.engine).toBe('Через стандартную маршрутизацию роутера')
    expect(s.engineWarning).toContain('обновите агента')
    expect(s.engineWarning).not.toContain('не работает')
  })
})

// Перенос уже заведённой статики в HydraRoute Neo (оператор 28.10.2026).
describe('canMigrateRule', () => {
  const RUNNING = { hr_neo: { installed: true, running: true } }
  it('только статический маршрут и только при работающем Neo', () => {
    expect(canMigrateRule({ kind: 'static', id: 's1' }, RUNNING)).toBe(true)
    expect(canMigrateRule({ kind: 'dns', id: 'd1' }, RUNNING)).toBe(false)
    expect(canMigrateRule({ kind: 'static', id: 's1' }, { hr_neo: { installed: true, running: false } })).toBe(false)
    expect(canMigrateRule({ kind: 'static', id: 's1' }, null)).toBe(false)
  })
})

describe('migratePlanSummary', () => {
  it('называет сети целиком и куда они переезжают', () => {
    const s = migratePlanSummary({
      from: { name: 'Офис', kind: 'static' },
      route: { name: 'Офис', backend: 'hydraroute', targets: [{ value: '203.0.113.0/24' }, { value: '198.51.100.7/32' }] },
      can_apply: true,
      hash: 'h1',
    })
    expect(s.title).toBe('Офис')
    expect(s.targets).toBe('203.0.113.0/24, 198.51.100.7/32')
    expect(s.body).toContain('HydraRoute Neo')
    expect(s.body).toContain('203.0.113.0/24')
    expect(s.canApply).toBe(true)
    expect(s.hash).toBe('h1')
  })

  it('спор с другим правилом блокирует перенос и называется', () => {
    const s = migratePlanSummary({
      route: { name: 'Офис', targets: [{ value: '203.0.113.0/24' }] },
      overlaps: [{ severity: 'block', reason: 'target overlaps existing route bound to a different destination' }],
      can_apply: false,
    })
    expect(s.canApply).toBe(false)
    expect(s.notes[0]).toMatch(/^Мешает/)
  })
})
