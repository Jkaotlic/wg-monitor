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
import { SelfhostedScreen } from './SelfhostedScreen.jsx'
import { SelfhostedInstanceScreen } from './SelfhostedInstanceScreen.jsx'
import { SELFHOSTED_TEXTS } from '../selfhostedForm.js'
import { Overlay } from '../ui/Overlay.jsx'
import { FLEET_OVERLAYS, normalizeReturn } from '../nav.js'
import { jobTitle } from '../jobSteps.js'

export function routerContext(routers, routerID) {
  const current = routers.find((r) => r.id === routerID)
  // Статус берём из списка флота: экраны табов не грузят карточку роутера
  // сами, а спящему роутеру нужно обещать отложенный ответ, а не мгновенный.
  const asleep = current?.status === 'offline' || current?.status === 'sleeping'
  return { current, asleep }
}

// Подпись «назад» у слоя парка -- куда он вернёт: к списку роутеров (там
// Парк), во вкладку «Управление», на список своих серверов или к сводке
// роутеров (широкий экран без роутера). Старый возврат 'admin' ведёт к
// списку: «Обслуживания» как слоя больше нет.
export function returnLabel(returnTo) {
  const to = normalizeReturn(returnTo)
  if (to === 'fleet') return 'Мои роутеры'
  if (to === 'manage') return 'Управление'
  if (to === 'selfhosted') return 'Свои серверы'
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
  const layerOpener = (from) => (overlay, extra = {}) => dispatch({ type: 'overlay', overlay, params: { ...extra, returnTo: from } })
  // Новый роутер появляется в списке оболочки только после переспроса: без
  // него «Открыть роутер» открыл бы пустоту.
  const reloadRouters = () => Promise.resolve(refreshRouters ? refreshRouters() : undefined)

  // Экраны роутера глубже «Управления» закрываются обратно во вкладку.
  const toManage = () => dispatch({ type: 'overlay', overlay: 'manage' })

  // «Мои роутеры» на телефоне: у админа под списком -- Парк (v0.41; раньше
  // он жил в «Обслуживании» конкретного роутера). Слои парка возвращают
  // сюда же.
  if (nav.overlay === 'fleet') {
    return (
      <FleetOverlay
        routers={routers}
        currentID={nav.routerID}
        onPick={(id) => dispatch({ type: 'router', id })}
        onClose={close}
        shortcut={!nav.sheet}
        isAdmin={isAdmin}
        openSheet={openSheet}
        openLayer={layerOpener('fleet')}
        onOpenConnection={(id) => {
          dispatch({ type: 'router', id })
          dispatch({ type: 'overlay', overlay: 'agentconn' })
        }}
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
      // Свои VPN-серверы: список знает, откуда его открыли; экран сервера
      // возвращает на список вместе с этим возвратом (returnParams).
      // SSH-пароля в параметрах нет -- только id сервера.
      case 'selfhosted':
        return (
          <SelfhostedScreen
            backLabel={returnLabel(returnTo)}
            onClose={leave}
            onOpenInstance={(id) =>
              dispatch({ type: 'overlay', overlay: 'selfhostedinst', params: { instanceId: id, returnTo: 'selfhosted', returnParams: { returnTo } } })
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
      default:
        return <BackendDeployWait targetVersion={params.targetVersion} onBack={() => dispatch({ type: 'overlay', overlay: returnTo, unpin: true })} />
    }
  }

  if (nav.routerID == null) return null
  switch (nav.overlay) {
    // Кабинеты VPN роутера -- слой с адресом (?open=cabinet): обновление
    // страницы возвращает сюда же. Вкладка кабинета и выбранный вариант в
    // адрес не пишутся.
    case 'cabinet':
      return <CabinetScreen routerID={nav.routerID} routerName={current?.nickname} asleep={asleep} openSheet={openSheet} onClose={close} />
    case 'routes':
      return (
        <Overlay title="Маршруты" backLabel="VPN-туннели" onBack={close}>
          {/* rebindFrom -- VPN-туннель, с экрана которого пришли переносить
              правила: «Маршруты» сами откроют выбор цели. */}
          <RoutesTab routerID={nav.routerID} asleep={asleep} openSheet={openSheet} rebindFrom={params.rebindFrom ?? ''} />
        </Overlay>
      )
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
