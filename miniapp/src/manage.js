import { pluralRu } from './labels.js'
import { installedRows, versionsRows } from './versions.js'

// «Управление» без простыни (v0.50, спека п. 3.1): чипы-якоря и итоговые
// строки свёрнутых групп. Чип -- только к группе, которая есть у этой роли:
// чип, ведущий в пустоту, читался бы поломкой. «Доступ» -- админский раздел
// внутри «Настроек и доступа».
export function manageAnchors({ canRepair = false, isAdmin = false } = {}) {
  return [
    { id: 'mg-router', group: 'router', label: 'Роутер' },
    { id: 'mg-versions', group: 'versions', label: 'Версии' },
    canRepair && { id: 'mg-repair', group: 'repair', label: 'Починить' },
    { id: 'mg-settings', group: 'settings', label: 'Настройки' },
    isAdmin && { id: 'mg-access', group: 'settings', label: 'Доступ' },
  ].filter(Boolean)
}

// Известно ли хоть что-то из «что стоит на роутере». Три «сведений нет»
// подряд -- это одно «версии ещё не получены», а не три строки (п. 3.7).
export function versionsKnown(versions) {
  return installedRows(versions).some((r) => r.tone !== 'muted')
}

// Отстаёт ли агент, «Управление» не знает: это знает только /fleet (админ).
// Поэтому строка версий говорит версию агента и число новостей.
export function manageSummaries({ settings = null, versions = null, showReboot = false, agentReady = true, isAdmin = false } = {}) {
  const news = versionsRows(versions).length
  const agent = settings?.agent_version ? `агент ${settings.agent_version}` : ''
  const newsText = news ? `${news} ${pluralRu(news, 'обновление', 'обновления', 'обновлений')}` : 'обновлений нет'
  return {
    versions: !versionsKnown(versions) && !news ? 'версии ещё не получены' : [agent, newsText].filter(Boolean).join(' · '),
    repair: showReboot
      ? 'нужна перезагрузка роутера'
      : !agentReady
        ? 'агент старый — обслуживание после его обновления'
        : isAdmin
          ? 'службы, пакеты Entware, сброс DNS'
          : 'службы и пакеты Entware',
    settings: isAdmin ? 'пороги тревог, агент, доступ' : 'пороги тревог',
  }
}
