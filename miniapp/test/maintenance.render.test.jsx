// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ settings: null, versions: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
}))

const { SettingsSections } = await import('../src/screens/SettingsScreen.jsx')
const { MAINT_TEXTS } = await import('../src/maintenance.js')

const AWGM_ROW = { component: 'awgmgr', name: 'awg-manager', installed: '2.19.0+r2', available: '2.19.1' }
const OPERATOR = { role: 'operator', agent_version: 'v0.32.0' }
const VERSIONS = { rows: [AWGM_ROW], unknown: [], installed: { awgmgr: '2.19.0+r2', hrneo: '3.18.3', hrneo_installed: true } }

async function mount({ settings = OPERATOR, versions = VERSIONS, routerName = 'home' } = {}) {
  mocks.settings = settings
  mocks.versions = versions
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<SettingsSections routerID={2} routerName={routerName} asleep={false} openSheet={(s) => sheets.push(s)} />, root)
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return { root, sheets, unmount: () => { render(null, root); root.remove() } }
}

const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)
const click = (el) => act(async () => el.click())
const deliver = (sheet, result) => act(async () => sheet.onResult(result))

describe('обслуживание на экране настроек', () => {
  it('оператор обновляет awg-manager из строки новости', async () => {
    const { root, sheets, unmount } = await mount()
    await click(button(root, 'Обновить awg-manager'))
    expect(sheets[0].action).toBe('awgm_update')
    expect(sheets[0].body).toContain('2.19.0+r2 → 2.19.1')
    expect(sheets[0].confirmPhrase).toBe('')
    unmount()
  })

  it('службы, HydraRoute Neo и пакеты открывают свои листы', async () => {
    const { root, sheets, unmount } = await mount()
    await click(button(root, 'Перезапустить HydraRoute'))
    await click(button(root, 'Перезапустить awg-manager'))
    await click(button(root, 'Проверить и обновить HydraRoute Neo'))
    await click(button(root, 'Обновить пакеты Entware'))
    expect(sheets.map((s) => [s.action, s.args])).toEqual([
      ['service_restart', { name: 'hrneo' }],
      ['service_restart', { name: 'awgmgr' }],
      ['hrneo_update', {}],
      ['opkg_upgrade', {}],
    ])
    unmount()
  })

  it('старый агент: вместо кнопок обновления -- объяснение, службы остаются', async () => {
    const { root, unmount } = await mount({ settings: { role: 'owner', agent_version: 'v0.31.1' } })
    expect(button(root, 'Обновить awg-manager')).toBeUndefined()
    expect(button(root, 'Проверить и обновить HydraRoute Neo')).toBeUndefined()
    expect(root.textContent).toContain(MAINT_TEXTS.tooOld)
    expect(button(root, 'Перезапустить HydraRoute')).toBeDefined()
    unmount()
  })

  it('HydraRoute Neo не установлен -- кнопки его обновления нет', async () => {
    const { root, unmount } = await mount({ versions: { ...VERSIONS, installed: { hrneo_installed: false } } })
    expect(button(root, 'Проверить и обновить HydraRoute Neo')).toBeUndefined()
    expect(root.textContent).toContain(MAINT_TEXTS.hrneoMissing)
    unmount()
  })

  it('плашка перезагрузки из снимка, перезагрузка -- с набором имени', async () => {
    const { root, sheets, unmount } = await mount({ versions: { ...VERSIONS, reboot_hint: 'Сменился модуль ядра AmneziaWG — VPN-туннели поднимутся после перезагрузки роутера.' } })
    expect(root.textContent).toContain(MAINT_TEXTS.rebootBanner)
    await click(button(root, 'Перезагрузить роутер'))
    expect(sheets[0].action).toBe('service_restart')
    expect(sheets[0].args).toEqual({ name: 'router' })
    expect(sheets[0].confirmPhrase).toBe('home')
    unmount()
  })

  it('итог awgm_update с reboot_needed сразу показывает плашку', async () => {
    const { root, sheets, unmount } = await mount()
    expect(root.textContent).not.toContain(MAINT_TEXTS.rebootBanner)
    await click(button(root, 'Обновить awg-manager'))
    await deliver(sheets[0], {
      status: 'ok',
      output: JSON.stringify({ updated: true, from: '2.19.0+r2', to: '2.19.1', kmod_installed: '3.2', kmod_loaded: '3.1', reboot_needed: true }),
    })
    expect(root.textContent).toContain(MAINT_TEXTS.rebootBanner)
    expect(button(root, 'Перезагрузить роутер')).toBeDefined()
    unmount()
  })

  it('агент запретил перезагрузку -- вместо кнопки объяснение', async () => {
    const { root, sheets, unmount } = await mount({ versions: { ...VERSIONS, reboot_hint: 'x' } })
    await click(button(root, 'Перезагрузить роутер'))
    await deliver(sheets[0], { status: 'err', output: 'router reboot disabled in agent config' })
    expect(button(root, 'Перезагрузить роутер')).toBeUndefined()
    expect(root.textContent).toContain(MAINT_TEXTS.rebootForbidden)
    unmount()
  })

  it('мёртвый фид в итоге пакетов -- кнопка «Отключить фид»', async () => {
    const { root, sheets, unmount } = await mount()
    await click(button(root, 'Обновить пакеты Entware'))
    await deliver(sheets[0], {
      status: 'ok',
      output: '✅ Все пакеты актуальны — обновлять нечего.',
      payload: { failed_feeds: ['https://feed.example.com/aarch64-k3.10/Packages.gz'] },
    })
    expect(root.textContent).toContain('Все пакеты актуальны — обновлять нечего.')
    await click(button(root, 'Отключить фид'))
    const last = sheets[sheets.length - 1]
    expect(last.action).toBe('opkg_feed_disable')
    expect(last.args).toEqual({ url: 'https://feed.example.com/aarch64-k3.10' })
    unmount()
  })

  it('оператор ставит прошивку из строки новости -- с набором имени', async () => {
    const fwRow = { component: 'firmware', name: 'KeeneticOS', installed: '5.02.A.8.0-3', available: '5.02.A.9.0-0' }
    const { root, sheets, unmount } = await mount({ versions: { ...VERSIONS, rows: [fwRow] } })
    await click(button(root, 'Установить прошивку'))
    expect(sheets[0].action).toBe('firmware_install')
    expect(sheets[0].confirmPhrase).toBe('home')
    unmount()
  })

  // Имя роутера ещё не пришло (fleet-список не догрузился) -- confirmReady
  // на пустой фразе проходит без ввода (sheet.js), и сервер ответил бы
  // confirm_mismatch. Кнопки, которые требуют набор имени, не должны
  // рисоваться, пока имени нет.
  it('имя роутера не загружено -- кнопок перезагрузки и прошивки нет', async () => {
    const fwRow = { component: 'firmware', name: 'KeeneticOS', installed: '5.02.A.8.0-3', available: '5.02.A.9.0-0' }
    const { root, unmount } = await mount({
      routerName: '',
      versions: { ...VERSIONS, rows: [fwRow], reboot_hint: 'x' },
    })
    expect(root.textContent).toContain(MAINT_TEXTS.rebootBanner)
    expect(button(root, 'Перезагрузить роутер')).toBeUndefined()
    expect(button(root, 'Установить прошивку')).toBeUndefined()
    unmount()
  })

  it('без роли разделов обслуживания нет', async () => {
    const { root, unmount } = await mount({ settings: { role: '' } })
    expect(button(root, 'Обновить пакеты Entware')).toBeUndefined()
    expect(button(root, 'Обновить awg-manager')).toBeUndefined()
    unmount()
  })
})
