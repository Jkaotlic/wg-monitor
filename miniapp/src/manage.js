import { pluralRu } from './labels.js'
import { installedRows, versionsRows } from './versions.js'

// «Настройки» (v0.52, спека §2): четыре раздела вместо групп v0.50. Механика
// прежняя: свёрнутые разделы с итоговой строкой, раздел с заботой раскрыт и
// окрашен, пустой по правам раздел не рисуется, MANAGE_FOCUS раскрывает нужный
// по старой ссылке. Заголовки -- одни для экрана и для places.js.
export const MANAGE_SECTIONS = [
  { id: 'service', title: 'Обслуживание', chip: 'Обслуживание' },
  { id: 'people', title: 'Люди и уведомления', chip: 'Люди' },
  { id: 'agent', title: 'Роутер и агент', chip: 'Роутер и агент' },
  { id: 'danger', title: 'Опасное', chip: 'Опасное' },
]

export function manageSection(id) {
  return MANAGE_SECTIONS.find((s) => s.id === id) ?? null
}

// Чип -- только к разделу, который есть у роли: «Опасное» -- админу.
export function manageAnchors({ isAdmin = false } = {}) {
  return MANAGE_SECTIONS.filter((s) => s.id !== 'danger' || isAdmin).map((s) => ({ id: `mg-${s.id}`, group: s.id, label: s.chip }))
}

// Известно ли хоть что-то из «что стоит на роутере». Три «сведений нет»
// подряд -- это одно «версии ещё не получены», а не три строки (п. 3.7).
export function versionsKnown(versions) {
  return installedRows(versions).some((r) => r.tone !== 'muted')
}

// Забота живёт в «Обслуживании»: прошивка -- danger (необратима), перезагрузка
// и старый агент -- warn.
export function manageTones({ versions = null, showReboot = false, agentReady = true } = {}) {
  const firmware = versionsRows(versions).some((r) => r.component === 'firmware')
  return { service: firmware ? 'danger' : showReboot || !agentReady ? 'warn' : null, people: null, agent: null, danger: null }
}

export function manageSummaries({ settings = null, versions = null, showReboot = false, agentReady = true, isAdmin = false } = {}) {
  const news = versionsRows(versions).length
  const agent = settings?.agent_version ? `агент ${settings.agent_version}` : ''
  const newsText = news ? `${news} ${pluralRu(news, 'обновление', 'обновления', 'обновлений')}` : 'обновлений нет'
  const role = settings?.role ?? ''
  return {
    service: showReboot
      ? 'нужна перезагрузка роутера'
      : !agentReady
        ? 'агент старый — обслуживание после его обновления'
        : !versionsKnown(versions) && !news
          ? 'версии ещё не получены'
          : [agent, newsText].filter(Boolean).join(' · '),
    people: [settings?.notify_muted ? 'уведомления выключены' : 'уведомления включены', isAdmin ? 'доступ' : ''].filter(Boolean).join(' · '),
    agent: isAdmin ? 'панель роутера, пороги тревог, агент' : role === 'owner' ? 'панель роутера, пороги тревог' : 'пороги тревог',
    danger: 'перенаправить агента',
  }
}
