import { tabLabel } from './nav.js'
import { manageSection } from './manage.js'
import { DIAG_SECTIONS } from './diag.js'

// Указатели «зайдите в X → Y» (v0.52, спека §6) -- из одного места: место =
// вкладка + раздел + пункт, подписи -- из тех же констант, что заголовки
// экранов. Тест сверяет, что каждое место существует.
export const PLACES = {
  access: { tab: 'manage', section: 'people', item: 'Доступ', owner: 'screens/AccessSection.jsx' },
  notifyMe: { tab: 'manage', section: 'people', item: 'Писать мне об этом роутере', owner: 'screens/SettingsScreen.jsx' },
  service: { tab: 'manage', section: 'service' },
  agentConn: { tab: 'manage', section: 'agent', item: 'Подключение агента', owner: 'screens/RouterAdminSections.jsx' },
  inspect: { tab: 'diag', section: 'inspect', item: 'Осмотреть роутер', owner: 'screens/CheckToolsSections.jsx' },
  browser: { tab: 'park', sectionTitle: 'Серверы', item: 'Открыть в браузере', owner: 'screens/ParkSection.jsx' },
  vps: { tab: 'park', sectionTitle: 'Серверы', item: 'Свои VPS', owner: 'screens/ParkSection.jsx' },
  forgetPassword: { tab: 'park', item: 'Забыть пароль', owner: 'screens/ParkSection.jsx' },
}

function sectionTitle(tab, id) {
  if (tab === 'manage') return manageSection(id)?.title ?? null
  if (tab === 'diag') return DIAG_SECTIONS.find((s) => s.id === id)?.title ?? null
  return null
}

export function placeParts(key) {
  const p = PLACES[key]
  if (!p) return []
  return [tabLabel(p.tab), p.section ? sectionTitle(p.tab, p.section) : p.sectionTitle ?? null, p.item ?? null].filter(Boolean)
}

export function placeText(key) {
  return placeParts(key)
    .map((s) => `«${s}»`)
    .join(' → ')
}
