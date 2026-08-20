import { describe, it, expect } from 'vitest'
import { templateGroups, addPlanSummary, deletePlanSummary, parseManualTargets, templateChoice, skippedNote } from '../src/routeAdd.js'

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

  it('набор из одних гео-тегов без HR Neo применить нельзя, и экран говорит почему', () => {
    const c = templateChoice(GEO, { hrNeoRunning: false })
    expect(c.canApply).toBe(false)
    expect(c.reason).toContain('HR Neo')
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
