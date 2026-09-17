import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Секреты кабинетов и SSH-пароль не должны утекать мимо экрана: ни в консоль,
// ни в хранилище браузера, ни в навигацию. Статическая проверка ловит то, что
// рендер-тест не увидит: отладочный console.log, забытый при правке.
const FILES = [
  'cabinetKeys.js',
  'selfhostedForm.js',
  'useOnClose.js',
  'ui/SegmentTabs.jsx',
  'screens/CabinetScreen.jsx',
  'screens/CabinetSecrets.jsx',
  'screens/CabinetOptions.jsx',
  'screens/CabinetSelfhosted.jsx',
  'screens/CabinetIssue.jsx',
  'screens/SelfhostedScreen.jsx',
  'screens/SelfhostedInstanceScreen.jsx',
]
const read = (f) => readFileSync(fileURLToPath(new URL(`../src/${f}`, import.meta.url)), 'utf8')

describe('секреты не утекают', () => {
  it('нет console.*, localStorage, sessionStorage', () => {
    for (const f of FILES) {
      const src = read(f)
      expect(src, f).not.toMatch(/\bconsole\./)
      expect(src, f).not.toMatch(/localStorage|sessionStorage/)
    }
  })

  it('секрет не кладётся в параметры слоя или адрес', () => {
    for (const f of FILES) {
      const src = read(f)
      expect(src, f).not.toMatch(/params:\s*\{[^}]*(vpn_key|access_code|ssh_password|secret)/)
      expect(src, f).not.toMatch(/history\.(push|replace)State/)
    }
    const host = read('screens/OverlayHost.jsx')
    expect(host).not.toMatch(/ssh_password|vpn_key|access_code/)
  })

  it('в api.js ключ, код и пароль уходят только телом', () => {
    const api = read('api.js')
    expect(api).not.toMatch(/`[^`]*\$\{[^}]*(secret|vpn_key|access_code|ssh_password)[^}]*\}[^`]*`/)
  })
})
