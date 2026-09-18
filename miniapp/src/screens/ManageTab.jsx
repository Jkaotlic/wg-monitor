import { useContext } from 'preact/hooks'
import { AppContext } from '../appContext.js'
import { SettingsSections } from './SettingsScreen.jsx'
import { RouterAdminSections } from './RouterAdminSections.jsx'

// Вкладка «Управление» (v0.41). Раньше то же самое пряталось за шестерёнкой
// в шапке («Настройки») и за строкой «Администрирование» внизу «Сейчас»
// («Обслуживание и доступы»): оператор просил функциональную кнопку внизу
// для всех. Порядок: адрес панели (первым -- за ним приходят чаще всего),
// затем настройки и обслуживание роутера, затем админские разделы этого
// роутера. Парка здесь нет: он про весь флот и живёт на «Моих роутерах».
export function ManageTab({ routerID, routerName = '', isAdmin = false, asleep, openSheet, openLayer, onOpenAgentConfig, onOpenAgentConnection, onOpenDNSReset, onOpenPackages }) {
  const { wide } = useContext(AppContext)
  return (
    <div class="screen manage-tab">
      {/* На широком экране имя роутера уже стоит в шапке основной области. */}
      {!wide && <h1 class="screen-title">{routerName || 'Роутер'}</h1>}
      <p class="router-lastseen">Панель, настройки, обслуживание и доступы этого роутера.</p>
      <SettingsSections routerID={routerID} routerName={routerName} asleep={asleep} openSheet={openSheet} />
      <RouterAdminSections
        routerID={routerID}
        routerName={routerName}
        isAdmin={isAdmin}
        openSheet={openSheet}
        openLayer={openLayer}
        onOpenAgentConfig={onOpenAgentConfig}
        onOpenAgentConnection={onOpenAgentConnection}
        onOpenDNSReset={onOpenDNSReset}
        onOpenPackages={onOpenPackages}
      />
    </div>
  )
}
