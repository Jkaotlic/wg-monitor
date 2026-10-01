import { FleetOverlay } from './FleetOverlay.jsx'
import { RoutesTab } from './RoutesTab.jsx'
import { AgentConfigScreen } from './AgentConfigScreen.jsx'
import { DNSResetScreen } from './DNSResetScreen.jsx'
import { ProvisionWizard } from './ProvisionWizard.jsx'
import { JobProgress } from './JobProgress.jsx'
import { BackendDeployWait } from './BackendDeployWait.jsx'
import { AgentConnectionScreen } from './AgentConnectionScreen.jsx'
import { PackagesScreen } from './PackagesScreen.jsx'
import { CabinetScreen } from './CabinetScreen.jsx'
import { RepairScreen } from './RepairScreen.jsx'
import { SelfhostedScreen } from './SelfhostedScreen.jsx'
import { SelfhostedInstanceScreen } from './SelfhostedInstanceScreen.jsx'
import { Awg3PanelFormScreen } from './Awg3PanelFormScreen.jsx'
import { Awg3PanelScreen } from './Awg3PanelScreen.jsx'
import { SELFHOSTED_TEXTS } from '../selfhostedForm.js'
import { Overlay } from '../ui/Overlay.jsx'
import { FLEET_OVERLAYS, normalizeReturn, awg3ListParams, navPinned, fleetIsHome } from '../nav.js'
import { jobTitle } from '../jobSteps.js'
import { isStale } from '../staleness.js'

export function routerContext(routers, routerID) {
  const current = routers.find((r) => r.id === routerID)
  // Статус берём из списка флота: экраны табов не грузят карточку роутера
  // сами, а спящему роутеру нужно обещать отложенный ответ, а не мгновенный.
  const asleep = isStale(current)
  return { current, asleep }
}

// Подпись «назад» у слоя парка -- куда он вернёт: к списку роутеров (у
// админа «Роутеры парка», у остальных «Мои роутеры»), в «Парк», во вкладку
// «Настройки», в раздел «Серверы» или на экран панели. Старый возврат 'admin'
// ведёт к списку: «Обслуживания» как слоя больше нет.
export function returnLabel(returnTo, isAdmin = false) {
  const to = normalizeReturn(returnTo)
  if (to === 'park') return 'Парк'
  if (to === 'fleet') return isAdmin ? 'Роутеры парка' : 'Мои роутеры'
  if (to === 'manage') return 'Настройки'
  if (to === 'selfhosted') return 'Серверы'
  if (to === 'awg3panel') return 'Панель VPN-сервера'
  return 'Роутеры'
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
  const returnTo = normalizeReturn(params.returnTo ?? null)
  const leave = () => dispatch({ type: 'overlay', overlay: returnTo })
  // Новый роутер появляется в списке оболочки только после переспроса: без
  // него «Открыть роутер» открыл бы пустоту.
  const reloadRouters = () => Promise.resolve(refreshRouters ? refreshRouters() : undefined)

  // Слой в слое возвращает в родителя с его параметрами (v0.52).
  const toParent = () => dispatch({ type: 'overlay', overlay: params.returnTo, params: params.returnParams ?? undefined })

  // Экраны роутера глубже «Управления» закрываются обратно во вкладку.
  const toManage = () => dispatch({ type: 'overlay', overlay: 'manage' })

  // «Мои роутеры» на телефоне -- только список: Парк стал вкладкой (v0.48).
  // Старые слои парка с возвратом 'fleet' по-прежнему возвращают сюда.
  if (nav.overlay === 'fleet') {
    return (
      <FleetOverlay
        routers={routers}
        currentID={nav.routerID}
        onPick={(id) => dispatch({ type: 'router', id, keepTab: true })}
        // Без выбранного роутера список -- главный экран: уходить с него
        // некуда (fleetIsHome в nav.js), кнопки «Назад» нет.
        onClose={fleetIsHome(nav) ? undefined : close}
        shortcut={!nav.sheet}
        isAdmin={isAdmin}
      />
    )
  }

  if (FLEET_OVERLAYS.includes(nav.overlay)) {
    // Сервер ответит не-админу 404 на всё, что внутри; рисовать пустой мастер
    // незачем. Список своих серверов открывается и по адресу -- там слова
    // вместо пустоты.
    if (!isAdmin) {
      if (nav.overlay !== 'selfhosted') return null
      return (
        <Overlay title={SELFHOSTED_TEXTS.title} backLabel="Назад" onBack={close}>
          <p class="state">{SELFHOSTED_TEXTS.adminOnly}</p>
        </Overlay>
      )
    }
    switch (nav.overlay) {
      case 'provision':
        return (
          <ProvisionWizard
            backLabel={returnLabel(returnTo, isAdmin)}
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
            backLabel={returnLabel(returnTo, isAdmin)}
            onClose={leave}
            onDone={() => {
              reloadRouters()
            }}
            onOpenRouter={(id) => reloadRouters().then(() => dispatch({ type: 'router', id }))}
          />
        )
      // Свои VPN-серверы: список знает, откуда его открыли; экран сервера
      // возвращает на список вместе с этим возвратом (returnParams).
      // SSH-пароля в параметрах нет -- только id сервера.
      case 'selfhosted':
        return (
          <SelfhostedScreen
            part={params.part ?? 'all'}
            backLabel={returnLabel(returnTo, isAdmin)}
            onClose={leave}
            onOpenInstance={(id) =>
              dispatch({ type: 'overlay', overlay: 'selfhostedinst', params: { instanceId: id, returnTo: 'selfhosted', returnParams: { returnTo, part: params.part } } })
            }
            onOpenAwg3={(id) =>
              dispatch({ type: 'overlay', overlay: 'awg3panel', params: { panelId: id, returnTo: 'selfhosted', returnParams: { returnTo, part: params.part } } })
            }
            onAddAwg3={() =>
              dispatch({ type: 'overlay', overlay: 'awg3form', params: { panelId: '', returnTo: 'selfhosted', returnParams: { returnTo, part: params.part } } })
            }
          />
        )
      case 'selfhostedinst':
        return (
          <SelfhostedInstanceScreen
            key={params.instanceId ?? ''}
            instanceId={params.instanceId ?? ''}
            backLabel={returnLabel('selfhosted')}
            openSheet={openSheet}
            onClose={() => dispatch({ type: 'overlay', overlay: 'selfhosted', params: params.returnParams ?? { returnTo: null } })}
          />
        )
      // Экран awg3-панели: роутеры парка -- для «Выпустить на роутер»;
      // «Настройки панели» открывают форму с возвратом сюда же.
      case 'awg3panel':
        return (
          <Awg3PanelScreen
            key={params.panelId ?? ''}
            panelId={params.panelId ?? ''}
            routers={routers}
            backLabel={returnLabel(returnTo, isAdmin)}
            openSheet={openSheet}
            onClose={() => dispatch({ type: 'overlay', overlay: returnTo ?? 'selfhosted', params: params.returnParams ?? { returnTo: null } })}
            onOpenRouterTunnels={(id) => dispatch({ type: 'router', id, tab: 'tunnels' })}
            onEdit={(id) => dispatch({ type: 'overlay', overlay: 'awg3form', params: { panelId: id, returnTo: 'awg3panel', returnParams: params } })}
          />
        )
      // Форма awg3-панели: пароль и .p12 -- только в её состоянии, в
      // параметрах слоя -- id панели и куда вернуться.
      case 'awg3form':
        return (
          <Awg3PanelFormScreen
            key={params.panelId ?? ''}
            panelId={params.panelId ?? ''}
            backLabel={returnLabel(returnTo, isAdmin)}
            openSheet={openSheet}
            onClose={() => dispatch({ type: 'overlay', overlay: returnTo ?? 'selfhosted', params: params.returnParams ?? { returnTo: null } })}
            onDeleted={() => dispatch({ type: 'overlay', overlay: 'selfhosted', params: awg3ListParams(params) })}
          />
        )
      default:
        return <BackendDeployWait targetVersion={params.targetVersion} onBack={() => dispatch({ type: 'overlay', overlay: returnTo, unpin: true })} />
    }
  }

  if (nav.routerID == null) return null
  switch (nav.overlay) {
    // Кабинеты VPN роутера -- слой с адресом (?open=cabinet): обновление
    // страницы возвращает сюда же. Вкладка кабинета и выбранный вариант в
    // адрес не пишутся. Родитель рисует и свои слои в одной позиции дерева,
    // чтобы его состояние (снимок, вкладка кабинета) пережило слой в слое.
    case 'cabinet':
    case 'cabinetissue':
      return (
        <CabinetScreen
          routerID={nav.routerID}
          routerName={current?.nickname}
          asleep={asleep}
          openSheet={openSheet}
          onClose={close}
          layer={nav.overlay}
          layerParams={params}
          initialTab={nav.overlay === 'cabinet' ? params.tab : undefined}
          openLayer={(overlay, p) => dispatch({ type: 'overlay', overlay, params: p })}
          closeLayer={toParent}
          onPin={(on) => dispatch({ type: 'pin', pinned: on })}
          pinned={navPinned(nav)}
        />
      )
    case 'routes':
    case 'routeadd':
    case 'routepick': {
      const routesParams = nav.overlay === 'routes' ? params : params.returnParams ?? {}
      const fromTunnel = routesParams.returnTo === 'tunnel'
      return (
        <Overlay title="Маршруты" backLabel={fromTunnel ? 'VPN-туннель' : 'VPN-туннели'} onBack={nav.overlay === 'routes' ? () => dispatch({ type: 'back' }) : toParent}>
          {/* rebindFrom -- VPN-туннель, с экрана которого пришли переносить
              правила: «Маршруты» сами откроют выбор цели. */}
          <RoutesTab
            routerID={nav.routerID}
            asleep={asleep}
            openSheet={openSheet}
            rebindFrom={routesParams.rebindFrom ?? ''}
            layer={nav.overlay}
            layerParams={params}
            openLayer={(overlay, p) => dispatch({ type: 'overlay', overlay, params: p })}
            closeLayer={toParent}
          />
        </Overlay>
      )
    }
    case 'repair':
      return <RepairScreen routerID={nav.routerID} checkName={params.checkName ?? ''} lineName={params.lineName ?? ''} onClose={close} />
    // Настройки агента, подключение агента, сброс DNS и пакеты лежат слоем
    // глубже «Управления»: закрытие возвращает во вкладку, а не на «Сейчас».
    case 'agentcfg':
      return <AgentConfigScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={toManage} />
    case 'agentconn':
      // Адрес с open=agentconn может открыть и не-админ: слова вместо пустоты.
      if (!isAdmin) {
        return (
          <Overlay title="Подключение агента" backLabel="Назад" onBack={close}>
            <p class="state">Этот экран доступен только администратору.</p>
          </Overlay>
        )
      }
      return <AgentConnectionScreen routerID={nav.routerID} routerName={current?.nickname} onClose={toManage} />
    case 'dnsreset':
      return <DNSResetScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={toManage} />
    case 'packages':
      // Как и подключение агента: адрес может открыть и не-админ.
      if (!isAdmin) {
        return (
          <Overlay title="Пакеты по расписанию" backLabel="Назад" onBack={close}>
            <p class="state">Этот экран доступен только администратору.</p>
          </Overlay>
        )
      }
      return <PackagesScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} onClose={toManage} />
    default:
      return null
  }
}
