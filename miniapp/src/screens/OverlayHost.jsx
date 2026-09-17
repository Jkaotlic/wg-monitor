import { FleetOverlay } from './FleetOverlay.jsx'
import { SettingsScreen } from './SettingsScreen.jsx'
import { AdminOverlay } from './AdminOverlay.jsx'
import { RoutesTab } from './RoutesTab.jsx'
import { AgentConfigScreen } from './AgentConfigScreen.jsx'
import { DNSResetScreen } from './DNSResetScreen.jsx'
import { ProvisionWizard } from './ProvisionWizard.jsx'
import { JobProgress } from './JobProgress.jsx'
import { BackendDeployWait } from './BackendDeployWait.jsx'
import { AgentConnectionScreen } from './AgentConnectionScreen.jsx'
import { PackagesScreen } from './PackagesScreen.jsx'
import { Overlay } from '../ui/Overlay.jsx'
import { FLEET_OVERLAYS } from '../nav.js'
import { jobTitle } from '../jobSteps.js'

export function routerContext(routers, routerID) {
  const current = routers.find((r) => r.id === routerID)
  // Статус берём из списка флота: экраны табов не грузят карточку роутера
  // сами, а спящему роутеру нужно обещать отложенный ответ, а не мгновенный.
  const asleep = current?.status === 'offline' || current?.status === 'sleeping'
  return { current, asleep }
}

// Подпись «назад» у слоя парка -- куда он вернёт: в Обслуживание роутера
// или к сводке роутеров (широкий экран без выбранного роутера).
export function returnLabel(returnTo) {
  return returnTo === 'admin' ? 'Обслуживание' : 'Роутеры'
}

// Слой поверх вкладок. Один выбор для обеих раскладок: телефонная кладёт его
// крышкой поверх экрана, широкая -- в основную область рядом со списком.
export function OverlayHost({ nav, dispatch, routers, isAdmin, refreshRouters }) {
  const { current, asleep } = routerContext(routers, nav.routerID)
  const openSheet = (sheet) => dispatch({ type: 'sheet', sheet })
  const close = () => dispatch({ type: 'overlay', overlay: null })

  // Слои парка знают, откуда их открыли (overlayParams.returnTo), и туда же
  // возвращают. В параметрах -- только номер задания, заголовок и версия.
  const params = nav.overlayParams ?? {}
  const returnTo = params.returnTo ?? null
  const leave = () => dispatch({ type: 'overlay', overlay: returnTo })
  const layerOpener = (from) => (overlay, extra = {}) => dispatch({ type: 'overlay', overlay, params: { ...extra, returnTo: from } })
  // Новый роутер появляется в списке оболочки только после переспроса: без
  // него «Открыть роутер» открыл бы пустоту.
  const reloadRouters = () => Promise.resolve(refreshRouters ? refreshRouters() : undefined)

  if (nav.overlay === 'fleet') {
    return <FleetOverlay routers={routers} currentID={nav.routerID} onPick={(id) => dispatch({ type: 'router', id })} onClose={close} shortcut={!nav.sheet} />
  }

  if (FLEET_OVERLAYS.includes(nav.overlay)) {
    // Сервер ответит не-админу 404 на всё, что внутри; рисовать пустой мастер
    // незачем.
    if (!isAdmin) return null
    switch (nav.overlay) {
      case 'provision':
        return (
          <ProvisionWizard
            backLabel={returnLabel(returnTo)}
            onClose={leave}
            onRegistered={() => {
              reloadRouters()
            }}
            onBusy={(pinned) => dispatch({ type: 'pin', pinned })}
            onStarted={({ jobId, nickname }) =>
              dispatch({ type: 'overlay', overlay: 'job', params: { jobId, title: jobTitle('provision', nickname), returnTo }, unpin: true })
            }
          />
        )
      case 'job':
        return (
          <JobProgress
            jobId={params.jobId}
            title={params.title ?? ''}
            backLabel={returnLabel(returnTo)}
            onClose={leave}
            onDone={() => {
              reloadRouters()
            }}
            onOpenRouter={(id) => reloadRouters().then(() => dispatch({ type: 'router', id }))}
          />
        )
      default:
        return <BackendDeployWait targetVersion={params.targetVersion} onBack={() => dispatch({ type: 'overlay', overlay: returnTo, unpin: true })} />
    }
  }

  if (nav.routerID == null) return null
  switch (nav.overlay) {
    case 'settings':
      return <SettingsScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={close} />
    case 'admin':
      return (
        <AdminOverlay
          routerID={nav.routerID}
          routerName={current?.nickname}
          isAdmin={isAdmin}
          onClose={close}
          openSheet={openSheet}
          openLayer={layerOpener('admin')}
          onOpenAgentConfig={() => dispatch({ type: 'overlay', overlay: 'agentcfg' })}
          onOpenAgentConnection={() => dispatch({ type: 'overlay', overlay: 'agentconn' })}
          onOpenDNSReset={() => dispatch({ type: 'overlay', overlay: 'dnsreset' })}
          onOpenPackages={() => dispatch({ type: 'overlay', overlay: 'packages' })}
          onOpenRouter={(id) => dispatch({ type: 'router', id })}
          onOpenRouterConnection={(id) => {
            dispatch({ type: 'router', id })
            dispatch({ type: 'overlay', overlay: 'agentconn' })
          }}
        />
      )
    case 'routes':
      return (
        <Overlay title="Маршруты" backLabel="VPN-туннели" onBack={close}>
          <RoutesTab routerID={nav.routerID} asleep={asleep} openSheet={openSheet} />
        </Overlay>
      )
    // Настройки агента, подключение агента и сброс DNS лежат слоем глубже
    // обслуживания: закрытие возвращает туда, откуда экран открыли, а не на
    // таб роутера.
    case 'agentcfg':
      return <AgentConfigScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    case 'agentconn':
      // Адрес с open=agentconn может открыть и не-админ: слова вместо пустоты.
      if (!isAdmin) {
        return (
          <Overlay title="Подключение агента" backLabel="Назад" onBack={close}>
            <p class="state">Этот экран доступен только администратору.</p>
          </Overlay>
        )
      }
      return <AgentConnectionScreen routerID={nav.routerID} routerName={current?.nickname} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    case 'dnsreset':
      return <DNSResetScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    case 'packages':
      // Как и подключение агента: адрес может открыть и не-админ.
      if (!isAdmin) {
        return (
          <Overlay title="Пакеты по расписанию" backLabel="Назад" onBack={close}>
            <p class="state">Этот экран доступен только администратору.</p>
          </Overlay>
        )
      }
      return <PackagesScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} onClose={() => dispatch({ type: 'overlay', overlay: 'admin' })} />
    default:
      return null
  }
}
