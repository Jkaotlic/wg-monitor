import { describe, it, expect } from 'vitest'
import { templateGroups, addPlanSummary, deletePlanSummary } from '../src/routeAdd.js'

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
