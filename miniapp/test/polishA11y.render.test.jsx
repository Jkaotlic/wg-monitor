// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { TrafficPath, pathLabel } from '../src/components/TrafficPath.jsx'

// Доводка v0.52: схема пути трафика говорит состояние и скринридеру; пустого
// экрана вместо вкладки не бывает; уровни заголовков не вложены h2 в h2.
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub-router">router {id}</div> }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub-tunnels" /> }))
vi.mock('../src/screens/ChecksTab.jsx', () => ({ ChecksTab: () => <div class="stub-checks" /> }))
vi.mock('../src/screens/ManageTab.jsx', () => ({ ManageTab: () => <div class="stub-manage" /> }))
vi.mock('../src/screens/ParkTab.jsx', () => ({ ParkTab: () => <div class="stub-park" /> }))
const { TabBody } = await import('../src/screens/TabBody.jsx')
const { DiagGroup } = await vi.importActual('../src/screens/DiagTab.jsx')
const { Section, SectionHeading } = await import('../src/ui/Section.jsx')
const { ManageGroup } = await import('../src/ui/ManageGroup.jsx')

async function mount(node) {
  const root = document.createElement('div')
  await act(async () => render(node, root))
  return root
}

describe('TrafficPath: состояние -- в подписи и в тексте, а не за role=img', () => {
  const tunnels = [{ id: 'awg10', name: 'vpn-de', status: 'up', run_state: 'running' }]
  it('подпись называет состояние обеих веток', () => {
    expect(pathLabel({ tunnel: 'up', direct: 'up', via: 'vpn-de' }, false)).toBe('Путь трафика: заблокированное идёт через VPN-туннель «vpn-de»; остальное — напрямую, без VPN-туннеля')
    expect(pathLabel({ tunnel: 'down', direct: 'up', via: 'vpn-de' }, false)).toBe('Путь трафика: VPN-туннель «vpn-de» не отвечает; остальное — напрямую, без VPN-туннеля')
    expect(pathLabel({ tunnel: 'down', direct: 'up', via: '' }, false)).toContain('VPN-туннель не отвечает')
    expect(pathLabel({ tunnel: 'unknown', direct: 'unknown', via: '' }, true)).toBe('Путь трафика: про VPN-туннель роутер не сказал; прямой путь — неизвестно, роутер молчит')
  })
  it('упавший VPN-туннель: подпись говорит «не отвечает», текст веток доступен', async () => {
    const root = await mount(<TrafficPath traffic={{ mode: 'full', egress_tunnel_id: 'awg10', egress_tunnel_name: 'vpn-de' }} incidents={[{ check: 'tunnel', tunnel_id: 'awg10', active: true }]} tunnels={[{ ...tunnels[0], status: 'down' }]} stale={false} />)
    const el = root.querySelector('.traffic-path')
    expect(el.getAttribute('role')).toBe('group')
    expect(root.querySelector('[role=img]')).toBe(null)
    const kicker = root.querySelector('.tp-branch .tp-kicker').textContent
    // Подпись и видимый текст говорят одно и то же состояние.
    if (kicker === 'VPN-ТУННЕЛЬ МОЛЧИТ') expect(el.getAttribute('aria-label')).toContain('не отвечает')
    else expect(el.getAttribute('aria-label')).not.toContain('не отвечает')
    expect(el.getAttribute('aria-label')).not.toMatch(/^Схема: заблокированное идёт через/)
    // Украшения скрыты, слова -- нет.
    for (const svg of root.querySelectorAll('svg')) expect(svg.getAttribute('aria-hidden')).toBe('true')
    expect(root.querySelector('.tp-branches').closest('[aria-hidden=true]')).toBe(null)
  })
})

describe('TabBody: неизвестная вкладка -- «Роутер», а не пустой экран', () => {
  const routers = [{ id: 7, nickname: 'home', status: 'online' }]
  it.each(['nope', undefined, null, 'park'])('tab=%s', async (tab) => {
    const root = await mount(<TabBody nav={{ routerID: 7, tab, overlay: null, sheet: null }} dispatch={() => {}} routers={routers} isAdmin={false} />)
    expect(root.querySelector('.stub-router')?.textContent).toBe('router 7')
  })
  it('известные вкладки -- свои экраны', async () => {
    for (const [tab, cls] of [['tunnels', '.stub-tunnels'], ['diag', '.stub-checks'], ['manage', '.stub-manage']]) {
      const root = await mount(<TabBody nav={{ routerID: 7, tab, overlay: null, sheet: null }} dispatch={() => {}} routers={routers} isAdmin={false} />)
      expect(root.querySelector(cls)).toBeTruthy()
    }
  })
})

// Уровни: заголовок группы -- h2, разделы внутри группы -- h3 (вид -- классы).
// На живых экранах то же сторожит проверка 11 скрипта раскладки.
describe('уровни заголовков: h2 не вложен в группу с h2', () => {
  it('раздел сам по себе -- h2; подзаголовок -- h3', async () => {
    const root = await mount(<Section title="Раздел"><SectionHeading class="access-subtitle" deeper>Владелец</SectionHeading></Section>)
    expect(root.querySelector('h2.section-title').textContent).toBe('Раздел')
    expect(root.querySelector('h3.access-subtitle').textContent).toBe('Владелец')
  })
  it('в группе «Проверок»: группа h2, разделы h3 с прежним классом', async () => {
    const root = await mount(<DiagGroup id="net"><Section title="Раздельный DNS"><p>x</p></Section></DiagGroup>)
    expect([...root.querySelectorAll('h2')].map((h) => h.className)).toEqual(['diag-group-title'])
    expect(root.querySelector('h3.section-title').textContent).toBe('Раздельный DNS')
  })
  it('в группе «Настроек»: свёртка h2, разделы h3, подзаголовки h4', async () => {
    const root = await mount(<ManageGroup id="people" title="Люди и уведомления" open><Section title="Доступ"><SectionHeading class="access-subtitle" deeper>Владелец</SectionHeading></Section></ManageGroup>)
    expect([...root.querySelectorAll('h2')].map((h) => h.textContent)).toEqual(['Люди и уведомления'])
    expect(root.querySelector('h3.section-title').textContent).toBe('Доступ')
    expect(root.querySelector('h4.access-subtitle').textContent).toBe('Владелец')
  })
  it('в исходниках экранов групп нет своих <h2>: только Section/SectionHeading', () => {
    // Строкой, а не new URL(): под jsdom глобальный URL -- не узловой.
    const dir = join(dirname(fileURLToPath(import.meta.url)), '../src/screens') + '/'
    const strip = (src) => src.replace(/\{\/\*[\s\S]*?\*\/\}/g, '').replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1')
    // Файлы, чьи разделы рисуются внутри групп «Проверок» и «Настроек».
    const inGroups = ['AccessSection.jsx', 'SignalSections.jsx', 'CheckToolsSections.jsx', 'ExitCompare.jsx', 'RouterAdminSections.jsx', 'SettingsScreen.jsx', 'PackagesCard.jsx', 'HrneoBlock.jsx']
    const bad = inGroups.filter((f) => /<h[1-6]\b/.test(strip(readFileSync(dir + f, 'utf8'))))
    expect(bad).toEqual([])
    const diag = strip(readFileSync(dir + 'DiagTab.jsx', 'utf8'))
    expect(diag.match(/<h2\b/g)).toHaveLength(1)
    expect(readdirSync(dir)).toContain('ManageTab.jsx')
    expect(strip(readFileSync(dir + 'ManageTab.jsx', 'utf8'))).not.toMatch(/<h[2-6]\b/)
  })
})
