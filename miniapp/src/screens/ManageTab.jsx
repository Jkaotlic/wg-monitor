import { useContext } from 'preact/hooks'
import { AppContext } from '../appContext.js'
import { SettingsSections } from './SettingsScreen.jsx'
import { AdminRepairSections, AdminSettingsSections, AdminDangerZone } from './RouterAdminSections.jsx'

// Вкладка «Управление» (v0.41). Раньше то же самое пряталось за шестерёнкой
// в шапке («Настройки») и за строкой «Администрирование» внизу «Сейчас»
// («Обслуживание и доступы»): оператор просил функциональную кнопку внизу
// для всех. Парка здесь нет: он про весь флот и живёт на «Моих роутерах».
//
// v0.47: оператор счёл порядок нелогичным -- два бывших экрана стояли друг
// под другом, и родственное оказывалось в разных концах (пакеты Entware и
// «Пакеты по расписанию», опрос и «Настройки агента», справка посередине).
// Теперь группы по тому, зачем пришли: Роутер · Версии · Проверить ·
// Починить · Настройки и доступ, затем свёрнутое «Опасное», справка
// последней. Админские куски -- слотами в родственные группы. Не-админу
// слоты не передаются вовсе (null), а не пустыми компонентами: группа по
// ним решает, есть ли что показывать, и пустой заголовок не рисует.
export function ManageTab({ routerID, routerName = '', isAdmin = false, focusGroup = null, asleep, openSheet, openLayer, onOpenAgentConfig, onOpenAgentConnection, onOpenDNSReset, onOpenPackages }) {
  const { wide } = useContext(AppContext)
  const repairSlot = isAdmin ? <AdminRepairSections isAdmin onOpenDNSReset={onOpenDNSReset} onOpenPackages={onOpenPackages} /> : null
  const settingsSlot = isAdmin ? (
    <AdminSettingsSections
      routerID={routerID}
      isAdmin
      openSheet={openSheet}
      onOpenAgentConfig={onOpenAgentConfig}
      onOpenAgentConnection={onOpenAgentConnection}
    />
  ) : null
  const dangerSlot = isAdmin ? (
    <AdminDangerZone
      routerID={routerID}
      routerName={routerName}
      isAdmin
      openSheet={openSheet}
      openLayer={openLayer}
      onOpenAgentConnection={onOpenAgentConnection}
    />
  ) : null
  return (
    <div class="screen manage-tab">
      {/* На широком экране имя роутера уже стоит в шапке основной области. */}
      {!wide && <h1 class="screen-title">{routerName || 'Роутер'}</h1>}
      <p class="router-lastseen">Панель, версии, обслуживание и доступы этого роутера.</p>
      <SettingsSections
        routerID={routerID}
        routerName={routerName}
        asleep={asleep}
        openSheet={openSheet}
        isAdmin={isAdmin}
        focusGroup={focusGroup}
        repairSlot={repairSlot}
        settingsSlot={settingsSlot}
        dangerSlot={dangerSlot}
      />
    </div>
  )
}
