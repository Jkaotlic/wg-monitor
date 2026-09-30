// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Вкладка «Управление» (v0.47): разделы сгруппированы по тому, зачем пришли --
// от частого и безопасного к редкому и опасному. До этого два бывших экрана
// стояли друг под другом, и «Пакеты по расписанию» оказывались в девяти
// разделах от «Обслуживания», а справка -- посередине.
const mocks = vi.hoisted(() => ({ settings: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve(null),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
  fetchAgentConnection: () => Promise.resolve({ awgm_url: 'https://awg.example.com' }),
}))

const { ManageTab } = await import('../src/screens/ManageTab.jsx')

const noop = () => {}

async function mount(isAdmin) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <ManageTab
        routerID={2}
        routerName="home"
        isAdmin={isAdmin}
        openSheet={noop}
        openLayer={noop}
        onOpenAgentConfig={noop}
        onOpenAgentConnection={noop}
        onOpenDNSReset={noop}
        onOpenPackages={noop}
      />,
      root,
    )
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return root
}

// Порядок как его видит глаз: заголовки групп («# …») и разделов вперемешку.
// Содержимое свёрнутого «Опасного» глаз не видит -- только его summary.
function outline(root) {
  return [...root.querySelectorAll('.manage-group-title, .section-title, .danger-zone > summary')]
    .filter((el) => !el.closest('.danger-zone') || el.tagName === 'SUMMARY')
    .map((el) =>
    el.classList.contains('manage-group-title') ? `# ${el.textContent}` : el.textContent,
  )
}

describe('«Управление»: порядок групп', () => {
  it('админ: группы от частого к опасному, справка последней', async () => {
    mocks.settings = { role: 'admin', panel_known: true, panel_scope: 'public', panel_url: 'https://awg.example.com' }
    const root = await mount(true)
    expect(outline(root)).toEqual([
      '# Роутер',
      'Панель роутера',
      'Уведомления',
      '# Версии',
      'Что стоит на роутере',
      'Прошивка роутера',
      '# Починить',
      'Обслуживание',
      'Пакеты по расписанию',
      'Сброс DNS',
      '# Настройки и доступ',
      'Опрос и тревоги',
      'Настройки агента',
      'Подключение агента',
      'Доступ',
      'Опасное',
      'Что умеет приложение',
    ])
    render(null, root)
    root.remove()
  })

  it('владелец: те же группы без админских разделов и без «Опасного»', async () => {
    mocks.settings = { role: 'owner' }
    const root = await mount(false)
    expect(outline(root)).toEqual([
      '# Роутер',
      'Панель роутера',
      'Уведомления',
      '# Версии',
      'Что стоит на роутере',
      'Прошивка роутера',
      '# Починить',
      'Обслуживание',
      '# Настройки и доступ',
      'Опрос и тревоги',
      'Что умеет приложение',
    ])
    render(null, root)
    root.remove()
  })

  it('без права обслуживания группа «Починить» пропадает целиком, без пустого заголовка', async () => {
    mocks.settings = { role: 'viewer' }
    const root = await mount(false)
    const lines = outline(root)
    expect(lines).not.toContain('# Починить')
    expect(lines).not.toContain('Обслуживание')
    render(null, root)
    root.remove()
  })
})
