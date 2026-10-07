import { useContext } from 'preact/hooks'
import { AppContext } from '../appContext.js'
import { SettingsSections } from './SettingsScreen.jsx'
import { AdminRepairSections, AdminSettingsSections, AdminAccessSection, AdminDangerZone } from './RouterAdminSections.jsx'

// Вкладка «Настройки» (v0.52): четыре раздела по задаче человека --
// Обслуживание · Люди и уведомления · Роутер и агент · Опасное (последнее --
// админу), справка внизу. Админские куски вставляются слотами в родственные
// разделы. Не-админу слоты не передаются вовсе (null), а не пустыми
// компонентами: раздел по ним решает, есть ли что показывать, и пустой
// заголовок не рисует. Парка здесь нет: он про весь флот.
export function ManageTab({ routerID, routerName = '', isAdmin = false, focusGroup = null, focusNonce = 0, asleep, openSheet, openLayer, onOpenAgentConfig, onOpenAgentConnection, onOpenDNSReset, onOpenPackages, onOpenPorthop, onOpenSpace }) {
  const { wide } = useContext(AppContext)
  const serviceSlot = isAdmin ? <AdminRepairSections isAdmin onOpenDNSReset={onOpenDNSReset} onOpenPackages={onOpenPackages} onOpenPorthop={onOpenPorthop} onOpenSpace={onOpenSpace} /> : null
  const peopleSlot = isAdmin ? <AdminAccessSection routerID={routerID} openSheet={openSheet} /> : null
  const agentSlot = isAdmin ? (
    <AdminSettingsSections routerID={routerID} isAdmin openSheet={openSheet} onOpenAgentConfig={onOpenAgentConfig} onOpenAgentConnection={onOpenAgentConnection} />
  ) : null
  const dangerSlot = isAdmin && routerName ? (
    <AdminDangerZone routerID={routerID} routerName={routerName} isAdmin openSheet={openSheet} openLayer={openLayer} onOpenAgentConnection={onOpenAgentConnection} />
  ) : null
  return (
    <div class="screen manage-tab">
      {/* На широком экране имя роутера уже стоит в шапке основной области. */}
      {!wide && <h1 class="screen-title">{routerName || 'Роутер'}</h1>}
      <p class="router-lastseen">Обслуживание, люди и уведомления, панель и агент этого роутера.</p>
      <SettingsSections
        routerID={routerID}
        routerName={routerName}
        asleep={asleep}
        openSheet={openSheet}
        isAdmin={isAdmin}
        focusGroup={focusGroup}
        focusNonce={focusNonce}
        serviceSlot={serviceSlot}
        peopleSlot={peopleSlot}
        agentSlot={agentSlot}
        dangerSlot={dangerSlot}
      />
    </div>
  )
}
