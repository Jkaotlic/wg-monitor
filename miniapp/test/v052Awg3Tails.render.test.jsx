// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const PANEL = { id: 'old', label: 'Старый пароль', issuers: [{ telegram_user_id: 4242, granted_at: '2026-09-30T10:00:00Z' }] }
vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchAwg3Peers: () => Promise.reject(new real.ApiError(409, 'awg3_bad_password', 'x', 'Неверный пароль панели — пересохраните учётные данные')),
    fetchAwg3Panels: () => Promise.resolve({ panels: [PANEL] }),
  }
})
const { Awg3PanelScreen } = await import('../src/screens/Awg3PanelScreen.jsx')
const { CabinetAwg3 } = await import('../src/screens/CabinetAwg3.jsx')
const { ISSUER_PANEL_DOWN } = await import('../src/awg3Panel.js')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  await flush()
  await flush()
  return root
}

describe('v0.52 §8: хвосты v0.51', () => {
  it('панель недоступна -- допуск всё равно виден и правится', async () => {
    const root = await mount(<Awg3PanelScreen panelId="old" routers={[]} openSheet={() => {}} onClose={() => {}} />)
    expect(root.querySelector('.awg3-banner')).toBeTruthy()
    expect(root.textContent).toContain('Кто может выпускать конфиги')
    expect(root.textContent).toContain('4242')
    render(null, root)
    root.remove()
  })
  it('недоступная панель в списке выпуска: допущенному -- «сообщите администратору»', async () => {
    const root = await mount(<CabinetAwg3 panels={[{ id: 'old', label: 'Старый пароль', unavailable: true, ifaces: [] }]} onPick={() => {}} admin={false} />)
    expect(root.textContent).toContain(ISSUER_PANEL_DOWN)
    render(null, root)
    root.remove()
  })
})
