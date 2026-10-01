import { readFileSync, readdirSync, statSync } from 'node:fs'
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
    if (p.sectionTitle) expect(readFileSync(SRC + p.owner, 'utf8'), `${key}: раздела «${p.sectionTitle}» нет в ${p.owner}`).toContain(`title="${p.sectionTitle}"`)
    if (p.item2) expect(readFileSync(SRC + p.owner2, 'utf8'), `${key}: подписи «${p.item2}» нет в ${p.owner2}`).toContain(p.item2)
    if (p.item) expect(readFileSync(SRC + p.owner, 'utf8'), `${key}: подписи «${p.item}» нет в ${p.owner}`).toContain(p.item)
  })

  it('текст указателя', () => {
    expect(placeText('access')).toBe('«Настройки» → «Люди и уведомления» → «Доступ»')
    expect(placeText('inspect')).toBe('«Проверки» → «Осмотр изнутри» → «Осмотреть роутер»')
    expect(placeText('service')).toBe('«Настройки» → «Обслуживание»')
    expect(placeText('нет такого')).toBe('')
  })

  // Указатели «зайдите в X → Y» -- только из places.js: рукописный устаревает
  // молча. Сканируются строки и разметка, не комментарии.
  it('рукописных указателей вне places.js нет', () => {
    const strip = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1')
    const walk = (dir) =>
      readdirSync(dir).flatMap((f) => {
        const p = dir + f
        if (statSync(p).isDirectory()) return walk(p + '/')
        return /\.(js|jsx)$/.test(f) && p !== SRC + 'places.js' ? [p] : []
      })
    const bad = walk(SRC).filter((p) => /→\s*«|раздел «|экране «/.test(strip(readFileSync(p, 'utf8'))))
    expect(bad.map((p) => p.slice(SRC.length))).toEqual([])
  })

  it('известные битые указатели берут место отсюда', () => {
    for (const f of ['fleetBatch.js', 'provisionWizard.js', 'revive.js', 'agentJobs.js', 'screens/NoAccess.jsx', 'screens/AccessSection.jsx', 'screens/RouterDetail.jsx', 'screens/LoginScreen.jsx', 'selfhostedForm.js']) {
      const src = readFileSync(SRC + f, 'utf8')
      expect(src, f).toContain('placeText(')
      expect(src, f).not.toMatch(/«Управление» →|вкладке «Управление»|на экране\s+«Доступ»/)
    }
  })
})
