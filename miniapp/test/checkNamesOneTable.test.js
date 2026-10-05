import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import NAMES from '../../internal/backend/alerts/check_names.json'
import { checkLabel, legendLabel } from '../src/labels.js'
import { checkRows } from '../src/diag.js'

// v0.56, B4: одна таблица подписей проверок -- общая с ботом
// (alerts/check_names.json; покрытие ключей агента проверяет Go-тест).
describe('B4: одна таблица подписей', () => {
  it('«Роутер»/«Проверки»/легенда зовут каждую проверку подписью из таблицы', () => {
    for (const [key, label] of Object.entries(NAMES)) {
      expect(checkLabel(key)).toBe(label)
      expect(legendLabel(key)).toBe(label)
    }
  })

  it('имена -- с заглавной, без латиницы вне «VPN» и «DNS»', () => {
    for (const label of Object.values(NAMES)) {
      expect(label[0]).toBe(label[0].toUpperCase())
      expect(label.replace(/VPN|DNS/g, '')).not.toMatch(/[A-Za-z_]/)
    }
  })

  it('строки «Проверок» подписаны из таблицы, а не своим словарём', () => {
    const rows = checkRows({
      checks: ['dns', 'dns_ru', 'external_reach', 'hydraroute', 'awg_manager'].map((n) => ({ check_name: n, status: 'ok', ts: new Date().toISOString() })),
      tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 5 }],
      router: { last_seen_age_sec: 10 },
    })
    for (const r of rows) expect(r.title).toBe(NAMES[r.key])
    expect(rows.length).toBeGreaterThanOrEqual(6)
  })

  it('в исходниках нет второго словаря подписей', () => {
    for (const f of readdirSync(new URL('../src', import.meta.url))) {
      if (!f.endsWith('.js')) continue
      const src = readFileSync(new URL(`../src/${f}`, import.meta.url), 'utf8')
      expect(src, f).not.toMatch(/ROW_TITLES|LEGEND_LABEL/)
    }
  })
})
