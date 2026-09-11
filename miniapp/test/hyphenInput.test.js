import { describe, it, expect } from 'vitest'
import { normalizeSiteInput, looksLikeSite } from '../src/routeLookup.js'
import { confirmReady } from '../src/sheet.js'
import { parseManualTargets } from '../src/routeAdd.js'

// Текст приложения несёт неразрывный дефис U+2011 в термине «VPN-туннель», а
// скопированное откуда угодно -- ещё и U+2010. Вставленное обратно во ввод
// обязано значить то же, что набранное руками: роутер U+2011 в имени сайта
// не примет, а подтверждение не совпадёт с именем роутера.
const NBH = '‑'

describe('ввод с неразрывным дефисом', () => {
  it('имя сайта в «Куда пойдёт сайт» -- с обычным дефисом', () => {
    expect(normalizeSiteInput(`my${NBH}site.ru`)).toBe('my-site.ru')
    expect(normalizeSiteInput('https://my‐site.ru/path')).toBe('my-site.ru')
    expect(looksLikeSite(normalizeSiteInput(`my${NBH}site.ru`))).toBe(true)
  })

  it('подтверждение набором сравнивает дефисы по сути -- с обеих сторон', () => {
    expect(confirmReady({ confirmPhrase: `home${NBH}1` }, 'home-1')).toBe(true)
    expect(confirmReady({ confirmPhrase: 'home-1' }, `home${NBH}1`)).toBe(true)
    expect(confirmReady({ confirmPhrase: 'home-1' }, 'home1')).toBe(false)
  })

  it('ручные цели правила -- с обычным дефисом', () => {
    const p = parseManualTargets(`my${NBH}site.ru, vpn‐nl.example.com`)
    expect(p.kind).toBe('dns')
    expect(p.targets).toEqual(['my-site.ru', 'vpn-nl.example.com'])
  })
})
