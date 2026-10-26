// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Улучшение аудита 26.10: сбой чтения автопочинки не прячет блок целиком --
// экран говорит о сбое и даёт повторить.
const mocks = vi.hoisted(() => ({ fail: true, calls: 0 }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  getAutorepair: () => {
    mocks.calls++
    if (mocks.fail) return Promise.reject(new Error('net'))
    return Promise.resolve({
      enabled: false, provider: '', option: '', allow_relocate: false, can_edit: true,
      suggested: { provider: 'amnezia', option: 'nl', why: 'так он был выпущен' },
      sources: [{ provider: 'amnezia', label: 'Amnezia Premium', ok: true, options: [{ id: 'nl', label: 'Нидерланды' }] }],
    })
  },
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  listAutorepair: () => Promise.resolve({ tunnels: {}, reasons: {} }),
}))
const SNAP = {
  tunnels: [{ id: 'awg12', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 }],
  policies: [],
}
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))
const { TunnelScreen } = await import('../src/screens/TunnelScreen.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => { mocks.fail = true; mocks.calls = 0 })

describe('автопочинка: ошибка загрузки', () => {
  it('блок остаётся: фраза о сбое и «Повторить», повтор возвращает обычный блок', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<TunnelScreen routerID={4} asleep={false} snapshot={SNAP} tunnelID="awg12" role="owner" openSheet={() => {}} onClose={() => {}} />, root))
    await flush()
    const line = root.querySelector('.tunnel-autorepair-error')
    expect(line).toBeTruthy()
    expect(line.textContent).toContain('Не удалось узнать, включена ли автопочинка.')
    mocks.fail = false
    await act(async () => [...line.querySelectorAll('button')].find((b) => b.textContent === 'Повторить').click())
    await flush()
    expect(mocks.calls).toBe(2)
    expect(root.querySelector('.tunnel-autorepair-error')).toBe(null)
    expect(root.querySelector('.tunnel-autorepair').textContent).toBe('Включить автопочинку')
    render(null, root)
    root.remove()
  })

  // Тому, кому править нельзя (роль известна и не owner/admin), строка про сбой
  // бесполезна: повторить и включить он всё равно не сможет.
  it('зритель без прав правки сбоя не видит; неизвестная роль -- как раньше', async () => {
    for (const [role, shown] of [['viewer', false], [undefined, true], ['admin', true]]) {
      const root = document.createElement('div')
      document.body.appendChild(root)
      await act(async () => render(<TunnelScreen routerID={4} asleep={false} snapshot={SNAP} tunnelID="awg12" role={role} openSheet={() => {}} onClose={() => {}} />, root))
      await flush()
      expect(!!root.querySelector('.tunnel-autorepair-error')).toBe(shown)
      render(null, root)
      root.remove()
    }
  })
})
