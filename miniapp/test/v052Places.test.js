import { readFileSync } from 'node:fs'
import { describe, it, expect } from 'vitest'
import { PLACES, placeParts, placeText } from '../src/places.js'
import { TABS, PARK_TAB, tabLabel } from '../src/nav.js'
import { MANAGE_SECTIONS } from '../src/manage.js'
import { DIAG_SECTIONS } from '../src/diag.js'

const SRC = new URL('../src/', import.meta.url).pathname
const SECTIONS = { manage: MANAGE_SECTIONS, diag: DIAG_SECTIONS }

describe('places.js: все места существуют', () => {
  it.each(Object.keys(PLACES))('%s', (key) => {
    const p = PLACES[key]
    expect([...TABS, PARK_TAB]).toContain(p.tab)
    expect(placeParts(key)[0]).toBe(tabLabel(p.tab))
    if (p.section) {
      const section = (SECTIONS[p.tab] ?? []).find((s) => s.id === p.section)
      expect(section, `${key}: раздела ${p.section} нет на вкладке ${p.tab}`).toBeTruthy()
      expect(placeParts(key)[1]).toBe(section.title)
    }
    if (p.item) expect(readFileSync(SRC + p.owner, 'utf8'), `${key}: подписи «${p.item}» нет в ${p.owner}`).toContain(p.item)
  })

  it('текст указателя', () => {
    expect(placeText('access')).toBe('«Настройки» → «Люди и уведомления» → «Доступ»')
    expect(placeText('inspect')).toBe('«Проверки» → «Осмотр изнутри» → «Осмотреть роутер»')
    expect(placeText('service')).toBe('«Настройки» → «Обслуживание»')
    expect(placeText('нет такого')).toBe('')
  })

  it('известные битые указатели берут место отсюда', () => {
    for (const f of ['fleetBatch.js', 'provisionWizard.js', 'revive.js', 'agentJobs.js', 'screens/NoAccess.jsx', 'screens/AccessSection.jsx', 'screens/RouterDetail.jsx']) {
      const src = readFileSync(SRC + f, 'utf8')
      expect(src, f).toContain('placeText(')
      expect(src, f).not.toMatch(/«Управление» →|вкладке «Управление»|на экране\s+«Доступ»/)
    }
  })
})
