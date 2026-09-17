import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// В .conf приватный ключ. Статическая проверка ловит то, что рендер-тест не
// увидит: отладочный console.log, хранилище браузера, конфиг в навигации.
const FILES = [
  'commandWait.js',
  'tunnelDelete.js',
  'confImport.js',
  'hrneoBlock.js',
  'screens/TunnelScreen.jsx',
  'screens/ConfImportScreen.jsx',
  'screens/HrneoBlock.jsx',
  'screens/TunnelsTab.jsx',
]
const read = (f) => readFileSync(fileURLToPath(new URL(`../src/${f}`, import.meta.url)), 'utf8')

describe('конфиг не утекает', () => {
  it('нет console.*, хранилища браузера, записи в историю', () => {
    for (const f of FILES) {
      const src = read(f)
      expect(src, f).not.toMatch(/\bconsole\./)
      expect(src, f).not.toMatch(/localStorage|sessionStorage/)
      expect(src, f).not.toMatch(/history\.(push|replace)State/)
    }
  })

  it('содержимое конфига -- только в ref экрана загрузки', () => {
    const screen = read('screens/ConfImportScreen.jsx')
    expect(screen).toMatch(/confRef\.current = b64/)
    expect(screen).not.toMatch(/set\w+\(\s*(b64|conf)\s*\)/)
    expect(screen).not.toMatch(/useState\([^)]*(b64|conf)/i)
    expect(screen).not.toMatch(/dispatch\(|params:/)
    for (const f of ['screens/TunnelsTab.jsx', 'screens/TabBody.jsx', 'screens/OverlayHost.jsx', 'nav.js', 'navUrl.js']) {
      expect(read(f), f).not.toMatch(/conf_b64|confB64|confRef/)
    }
  })

  // Контракт части 1: токен предпросмотра стоит в пути опроса
  // GET tunnels/import/{token} -- это его единственное место в адресе.
  // Конфиг в адрес не попадает никогда.
  // Ревью: конфиг не должен попасть ключом объекта (состояние, параметры
  // слоя, спред) -- ни в экране загрузки, ни в навигации.
  it('ключей conf / conf_b64 вне api.js нет', () => {
    for (const f of ['screens/ConfImportScreen.jsx', 'screens/TunnelsTab.jsx', 'screens/TabBody.jsx', 'screens/OverlayHost.jsx', 'nav.js', 'navUrl.js', 'confImport.js']) {
      const src = read(f)
      expect(src, f).not.toMatch(/\bconf_b64\b/)
      expect(src, f).not.toMatch(/[{,]\s*conf\s*[:,}]/)
      expect(src, f).not.toMatch(/\.\.\.\s*conf(Ref)?\b/)
    }
  })

  it('в api.js конфиг уходит только телом, токен -- только в путь опроса', () => {
    const api = read('api.js')
    const templates = api.match(/`[^`]*`/g) ?? []
    expect(templates.filter((t) => /\$\{[^}]*conf/i.test(t))).toEqual([])
    const withPreview = templates.filter((t) => /\$\{[^}]*(token|previewID)[^}]*\}/i.test(t))
    expect(withPreview).toEqual(['`/routers/${routerID}/tunnels/import/${encodeURIComponent(previewID)}`'])
  })
})
