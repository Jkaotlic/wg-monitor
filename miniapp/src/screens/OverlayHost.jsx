import { FleetOverlay } from './FleetOverlay.jsx'
import { SettingsScreen } from './SettingsScreen.jsx'
import { AdminOverlay } from './AdminOverlay.jsx'
import { RoutesTab } from './RoutesTab.jsx'
import { AgentConfigScreen } from './AgentConfigScreen.jsx'
import { DNSResetScreen } from './DNSResetScreen.jsx'
import { Overlay } from '../ui/Overlay.jsx'

export function routerContext(routers, routerID) {
  const current = routers.find((r) => r.id === routerID)
  // Статус берём из списка флота: экраны табов не грузят карточку роутера
  // сами, а спящему роутеру нужно обещать отложенный ответ, а не мгновенный.
  const asleep = current?.status === 'offline' || current?.status === 'sleeping'
  return { current, asleep }
}

// Слой поверх вкладок. Один выбор для обеих раскладок: телефонная кладёт его
// крышкой поверх экрана, широкая -- в основную область рядом со списком.
export function OverlayHost({ nav, dispatch, routers, isAdmin }) {
  const { current, asleep } = routerContext(routers, nav.routerID)
  const openSheet = (sheet) => dispatch({ type: 'sheet', sheet })
  const close = () => dispatch({ type: 'overlay', overlay: null })

  if (nav.overlay === 'fleet') {
    return <FleetOverlay routers={routers} currentID={nav.routerID} onPick={(id) => dispatch({ type: 'router', id })} onClose={close} />
  }
  if (nav.routerID == null) return null
  switch (nav.overlay) {
    case 'settings':
      return <SettingsScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={close} />
    case 'admin':
      return (
        <AdminOverlay
          routerID={nav.routerID}
          isAdmin={isAdmin}
          onClose={close}
          openSheet={openSheet}
          onOpenAgentConfig={() => dispatch({ type: 'overlay', overlay: 'agentcfg' })}
          onOpenDNSReset={() => dispatch({ type: 'overlay', overlay: 'dnsreset' })}
          onOpenRouter={(id) => dispatch({ type: 'router', id })}
        />
      )
    case 'routes':
      return (
        <Overlay title="Маршруты" backLabel="VPN-туннели" onBack={close}>
          <RoutesTab routerID={nav.routerID} asleep={asleep} openSheet={openSheet} />
        </Overlay>
      )
    // Настройки агента и сброс DNS лежат слоем глубже обслуживания: закрытие
    // возвращает туда, откуда экран открыли, а не на таб роутера.
    case 'agentcfg':
      return <AgentConfigScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    case 'dnsreset':
      return <DNSResetScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    default:
      return null
  }
}
