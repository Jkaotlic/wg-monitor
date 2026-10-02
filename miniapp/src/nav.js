// Навигация мини-аппа -- один чистый редьюсер, а не россыпь useState по
// компонентам. Причина: слоёв стало четыре (таб, оверлей, шит и выбранный
// роутер), а кнопка "назад" у Telegram одна, и решать, что она закрывает,
// должно одно место.
// v0.52: вкладки названы задачей человека -- «Роутер», «VPN-туннели»,
// «Проверки», «Настройки» (+ «Парк» админу). «Что было» стало видом
// «Проверок»: ключ events остаётся псевдонимом -- ссылки из отправленных
// тревог живут месяцами.
import { STRIP_MAX, landingRouterID } from './routerPick.js'

export const TABS = ['router', 'tunnels', 'diag', 'manage']

// «Парк» (v0.48) -- вкладка админа, первая в панели: весь парк, от
// выбранного роутера не зависит и открывается без него. Раньше он жил хвостом
// под «Моими роутерами», и список роутеров становился экраном инструментов.
// Кому она видна, решает оболочка по is_admin -- тот же признак, по которому
// Парк показывался под списком; редьюсер вкладку не прячет.
export const PARK_TAB = 'park'

// barTabs -- что в нижней панели. Админу с роутером -- Парк и вкладки
// роутера; без роутера -- только Парк (выбор роутера -- имя в шапке).
// Нижней «Роутеры» больше нет: у выбора роутера один вход (v0.52).
export function barTabs({ isAdmin = false, routerID = null } = {}) {
  if (!isAdmin) return TABS
  return routerID != null ? [PARK_TAB, ...TABS] : [PARK_TAB]
}

const TAB_ALIASES = { routes: 'tunnels', events: 'diag' }

export function normalizeTab(tab) {
  return TAB_ALIASES[tab] ?? tab
}

// Вид «Проверок» по сырому ключу вкладки: прежняя вкладка events -- «Что было».
export function diagViewFor(rawTab) {
  return rawTab === 'events' ? 'history' : null
}

// Вид -- как фокус: ключ есть только со значением «Что было», чтобы прежние
// снимки навигации не меняли форму.
function withDiagView(state, view) {
  if (view === 'history') return { ...state, diagView: 'history' }
  if (!('diagView' in state)) return state
  const { diagView: _drop, ...rest } = state
  return rest
}

// Слои, ставшие вкладкой. Настройки (?open=settings) и «Обслуживание и
// доступы» (?open=admin) переехали во вкладку «Управление»; ссылки на них
// живут в уже отправленных уведомлениях месяцами и обязаны вести туда же.
// 'manage' -- возврат слоя («Ход работы» перенаправления) во вкладку.
export const OVERLAY_TABS = { settings: 'manage', admin: 'manage', manage: 'manage' }

// Раздел «Настроек», который раскрыть по старой ссылке или при возврате из
// слоя (v0.52: Обслуживание · Люди и уведомления · Роутер и агент · Опасное).
export const MANAGE_FOCUS = { settings: 'agent', admin: 'service', packages: 'service', dnsreset: 'service', agentcfg: 'agent', agentconn: 'agent' }

// Фокус -- как параметры слоя: ключ есть только когда он задан, чтобы
// прежние снимки навигации не меняли форму.
function withoutFocus(state) {
  if (!state || !('manageFocus' in state)) return state
  const { manageFocus: _drop, ...rest } = state
  return rest
}

function withFocus(state, focus) {
  // manageFocusSeq растёт при каждой постановке и переживает снятие фокуса:
  // повторный переход в ту же группу -- новое значение, экран раскрывает её снова.
  return focus ? { ...state, manageFocus: focus, manageFocusSeq: (state.manageFocusSeq ?? 0) + 1 } : state
}

// Старый возврат слоёв парка «в Обслуживание» ведёт теперь к списку роутеров:
// Парк живёт там.
export function normalizeReturn(returnTo) {
  return returnTo === 'admin' ? 'fleet' : returnTo
}

// Оверлеи, которые можно открыть по адресу. Все они -- слои над выбранным
// роутером; список роутеров («fleet») сюда не входит: это выбор, а не место.
// Прежде ссылка открывала только настройки (кнопка «Панель роутера» в
// тревоге); веб-управлению нужны обновление страницы и закладки на любой слой.
export const OPEN_OVERLAYS = ['routes', 'agentcfg', 'dnsreset', 'agentconn', 'packages', 'cabinet']

// Слои всего парка, а не роутера: мастер «Добавить роутер», «Ход работы»,
// ожидание раскатки бэкенда. Открываются и без выбранного роутера и в адрес
// не пишутся: мастер держит введённые пароли, ход работы -- номер задания,
// которое через 30 минут исчезнет, а ожидание раскатки после перезагрузки
// бессмысленно. Параметры слоя -- nav.overlayParams; returnTo -- слой, куда
// вернуть «назад». Паролей в параметрах не бывает никогда.
// «Серверы» Парка (selfhosted: «Свои VPS» и «Панели VPN-серверов») и экран
// одного сервера (selfhostedinst) --
// тоже слои парка: серверы общие для всех роутеров. Параметры экрана
// сервера -- id сервера и returnParams (куда вернуть сам список); SSH-пароля
// в параметрах не бывает никогда.
// awg3-панели (awg3panel) и их форма (awg3form) -- слои парка без адреса; в
// параметрах только id панели.
export const FLEET_OVERLAYS = ['provision', 'job', 'backenddeploy', 'selfhosted', 'selfhostedinst', 'awg3panel', 'awg3form']

// Слои парка, которые всё же пишутся в адрес: список своих серверов -- это
// место, а не процесс, и закладка на него имеет смысл. Открывается и без
// выбранного роутера (?open=selfhosted).
export const URL_FLEET_OVERLAYS = ['selfhosted']

// v0.52: бывшие локальные слои (useState внутри вкладок) -- оверлеи. «Назад»
// Telegram закрывает любой из них, а в адрес пишется только место-родитель:
// у каждого состояние, не переживающее перезагрузку (шаг мастера, выбранный
// файл, ход починки, карточка из снимка роутера).
//
// Слой вкладки: рисует сама вкладка -- ему нужен её снимок; открытие ставит
// вкладку, уход с вкладки слой закрывает.
export const TAB_LAYERS = { tunnel: 'tunnels', replace: 'tunnels', confimport: 'tunnels' }
// Слой в слое: рисует слой-родитель (снимок «Маршрутов», данные кабинета);
// «назад» возвращает в родителя с его параметрами.
export const CHILD_LAYERS = { routeadd: 'routes', routepick: 'routes', cabinetissue: 'cabinet' }
// Слой роутера без адреса: починка идёт заданием, экран лишь смотрит.
export const ROUTER_LAYERS = ['repair']
export const LOCAL_LAYERS = [...Object.keys(TAB_LAYERS), ...Object.keys(CHILD_LAYERS), ...ROUTER_LAYERS]

// localLayerDepth -- сколько слоёв без адреса открыто подряд, считая от
// верхнего: VPN-туннель → «Заменить конфиг» -- два, «Маршруты» → «Добавить
// сайт» -- один (у «Маршрутов» адрес свой). Столько записей-меток держит в
// истории браузера веб-управление (useNavURL): «назад» закрывает по слою.
export function localLayerDepth(state) {
  let depth = 0
  let overlay = state?.overlay ?? null
  let params = state?.overlayParams
  while (overlay && LOCAL_LAYERS.includes(overlay) && depth < 4) {
    depth++
    overlay = normalizeReturn(params?.returnTo ?? null)
    params = params?.returnParams
  }
  return depth
}

// Слои, которые закрепляются на время отправки: мастер «Добавить роутер» и
// выпуск конфига (уход посреди выпуска -- второй выпуск и занятое место).
export const PINNABLE_OVERLAYS = ['provision', 'cabinetissue']

// layerFamily -- слой верхнего уровня, к которому относится оверлей: сам слой
// или родитель слоя в слое. Вкладка, перечитывающая данные при закрытии слоя,
// не должна считать закрытием переход из родителя в его дочерний слой.
export function layerFamily(overlay) {
  return CHILD_LAYERS[overlay] ?? overlay ?? null
}

export function tabOwnsLayer(state) {
  const tab = TAB_LAYERS[state?.overlay]
  return tab != null && tab === state.tab
}

// Слои, которые «назад» и Esc не закрывают: во время раскатки бэкенда уходить
// некуда -- приложение без сервера не работает, а экран сам перезагрузит
// страницу или предложит «Вернуться» после таймаута.
export const PINNED_OVERLAYS = ['backenddeploy']

// navPinned -- можно ли сейчас уйти со слоя. Ожидание раскатки закреплено
// всегда; мастер «Добавить роутер» -- пока запрос в пути (overlayParams.pinned):
// уход в этот момент терял бы номер задания и выпущенный токен. Закреплённый
// слой не отпускают ни «назад», ни Esc, ни выбор роутера или вкладки, ни
// «назад» браузера; выпускает только действие overlay с unpin: true.
export function navPinned(state) {
  return PINNED_OVERLAYS.includes(state?.overlay) || state?.overlayParams?.pinned === true
}

// fleetIsHome -- список роутеров открыт без выбранного роутера: это главный
// экран, а не крышка. «Назад», Esc и кнопка Telegram его не закрывают --
// иначе человек попадал в пустое «Выберите роутер в списке» (18.09). Выбору
// роутера и слоям парка это не мешает: в отличие от navPinned, признак
// касается только ухода «назад».
export function fleetIsHome(state) {
  return state?.overlay === 'fleet' && state?.routerID == null && state?.tab !== PARK_TAB
}

// Параметры принадлежат слою: вместе с ним они уходят целиком (ключа нет),
// а не остаются null -- так прежние снимки навигации не меняют форму.
function withoutParams(state) {
  if (!state || !('overlayParams' in state)) return state
  const { overlayParams: _drop, ...rest } = state
  return rest
}

function withoutSheetBusy(state) {
  if (!('sheetBusy' in state)) return state
  const { sheetBusy: _drop, ...rest } = state
  return rest
}

// Подписи отделены от ключей намеренно: ключ -- адрес deep-link из тревог,
// подпись -- слова для человека (v0.52: по задаче, а не по инструменту).
const TAB_LABELS = {
  router: 'Роутер',
  tunnels: 'VPN-туннели',
  diag: 'Проверки',
  manage: 'Настройки',
  park: 'Парк',
}

export function tabLabel(tab) {
  return TAB_LABELS[tab] ?? tab
}

// Подпись в нижней панели -- та же: вкладок не больше пяти, «VPN-туннель»
// пишется полностью (словарь).
export function barLabel(tab) {
  return tabLabel(tab)
}

export function initialNav({ routerIDs = [], deepLinkID = null, isAdmin = false, routers = null, lastID = null } = {}) {
  const state = { routerID: null, tab: 'router', overlay: null, sheet: null }
  // Deep-link с тревоги ведёт на конкретный роутер, но не обходит доступ:
  // сервер отдаст 404, а клиент не должен делать вид, что чужой роутер открыт.
  if (deepLinkID != null && routerIDs.includes(deepLinkID)) {
    state.routerID = deepLinkID
    return state
  }
  // Пустой доступ -- отдельный экран, а не список из нуля строк.
  if (routerIDs.length === 0) return state
  if (routerIDs.length === 1) {
    state.routerID = routerIDs[0]
    return state
  }
  // Главный экран (спека §3): админ -- Парк; 2–5 -- роутер в беде, иначе
  // последний открытый; 6+ -- список. Ссылка на роутер, доступа к которому
  // нет, не подменяется молча другим роутером -- список честнее.
  if (isAdmin) {
    state.tab = PARK_TAB
    return state
  }
  if (deepLinkID == null && routerIDs.length <= STRIP_MAX) {
    state.routerID = landingRouterID({ routerIDs, routers, lastID })
    return state
  }
  state.overlay = 'fleet'
  return state
}

// deepLinkOverlay -- какой слой открыть по адресу. Только вместе с открытым
// роутером и только из OPEN_OVERLAYS: любое другое значение игнорируется, а
// не угадывается.
export function deepLinkOverlay(search, state) {
  if (state?.routerID == null) return null
  const open = new URLSearchParams(search).get('open')
  return OPEN_OVERLAYS.includes(open) ? open : null
}

export function navReducer(state, action) {
  switch (action.type) {
    // Список роутеров приходит с сервера уже после первого рендера, поэтому
    // стартовое состояние подставляется отдельным действием, а не считается
    // в useReducer -- иначе выбор "открыть роутер или показать список"
    // пришлось бы делать до того, как известно, что доступно.
    case 'init':
      if (action.source === 'popstate' && navPinned(state)) return state
      return action.state ?? state
    case 'tab': {
      const tab = normalizeTab(action.tab)
      if (!(TABS.includes(tab) || tab === PARK_TAB) || navPinned(state)) return state
      const view = diagViewFor(action.tab)
      // Вкладки широкой раскладки видны и над открытым оверлеем: нажатие на
      // вкладку -- это уход со слоя, а не смена вкладки под ним.
      if (action.closeOverlay) return withDiagView(withoutFocus({ ...withoutParams(state), tab, overlay: null, sheet: null }), view)
      // Слой вкладки принадлежит ей: уход на другую вкладку его закрывает.
      const layerTab = TAB_LAYERS[state.overlay]
      if (layerTab && layerTab !== tab) return withDiagView(withoutFocus({ ...withoutParams(state), tab, overlay: null, sheet: null }), view)
      return withDiagView(withoutFocus({ ...state, tab }), view)
    }
    case 'router': {
      if (navPinned(state)) return state
      // keepTab -- смена роутера из шапки (v0.50): вкладка роутера остаётся.
      // tab -- переход сразу на вкладку («Открыть VPN-туннели «ник»»).
      const wanted = action.tab ? normalizeTab(action.tab) : null
      const kept = action.keepTab && TABS.includes(state.tab) ? state.tab : null
      const tab = wanted && TABS.includes(wanted) ? wanted : kept ?? 'router'
      // Смена роутера из шапки держит и вкладку, и её вид («Что было»).
      const view = tab !== 'diag' ? null : wanted ? diagViewFor(action.tab) : state.diagView ?? null
      return withDiagView(withoutFocus({ ...withoutParams(state), routerID: action.id, tab, overlay: null, sheet: null }), view)
    }
    case 'overlay': {
      if (navPinned(state) && !action.unpin) return state
      const overlay = action.overlay ?? null
      // Возврат слоя парка во вкладку Парк: она есть и без роутера.
      if (overlay === PARK_TAB) return { ...withoutParams(state), tab: PARK_TAB, overlay: null, sheet: null }
      if (OVERLAY_TABS[overlay] && state.routerID != null) {
        return withFocus(
          { ...withoutParams(withoutFocus(state)), tab: OVERLAY_TABS[overlay], overlay: null, sheet: null },
          MANAGE_FOCUS[overlay] ?? MANAGE_FOCUS[state.overlay] ?? null,
        )
      }
      if (TAB_LAYERS[overlay] || ROUTER_LAYERS.includes(overlay)) {
        if (state.routerID == null) return state
        const tab = TAB_LAYERS[overlay] ?? state.tab
        const next = { ...withoutParams(withoutFocus(state)), tab, overlay, sheet: null }
        return action.params ? { ...next, overlayParams: action.params } : next
      }
      const parent = CHILD_LAYERS[overlay]
      if (parent) {
        if (state.overlay !== parent) return state
        return { ...withoutParams(state), overlay, overlayParams: { ...(action.params ?? {}), returnTo: parent, returnParams: state.overlayParams ?? null } }
      }
      const next = { ...withoutParams(state), overlay }
      if (!overlay && state.routerID != null && state.tab === 'manage') return withFocus(next, MANAGE_FOCUS[state.overlay] ?? null)
      return overlay && action.params ? { ...next, overlayParams: action.params } : next
    }
    // Сегмент «Сейчас | Что было» на «Проверках».
    case 'diagView':
      return state.tab === 'diag' ? withDiagView(state, action.view) : state
    // Переход в раздел «Настроек» (плашка «Есть обновления» на «Роутере»).
    case 'manage':
      if (state.routerID == null || navPinned(state)) return state
      return withFocus({ ...withoutParams(withoutFocus(state)), tab: 'manage', overlay: null, sheet: null }, action.section ?? null)
    case 'sheet': {
      // sheetSeq -- номер экземпляра листа, ключ его компонента. Новый лист
      // поверх открытого (без закрытия) обязан смонтироваться заново:
      // иначе набранное в первом -- имя, пароль root -- переехало бы во
      // второй, чужой роутер. Тот же объект листа номер не меняет.
      const sheet = action.sheet ?? null
      // Занятость принадлежит листу: новый лист или закрытие её снимают.
      const rest = sheet === state.sheet ? state : withoutSheetBusy(state)
      if (sheet && sheet !== state.sheet) return { ...rest, sheet, sheetSeq: (state.sheetSeq ?? 0) + 1 }
      return { ...rest, sheet }
    }
    case 'sheetBusy': {
      // Лист занят (запрос ушёл): «назад» его не закрывает. Ставит сам лист.
      if (!state.sheet) return state
      return action.busy ? { ...state, sheetBusy: true } : withoutSheetBusy(state)
    }
    // Закрепить мастер на время отправки. Флаг живёт в параметрах слоя и
    // уходит вместе с ним; паролей там по-прежнему нет.
    case 'pin': {
      if (!PINNABLE_OVERLAYS.includes(state.overlay)) return state
      const { pinned: _drop, ...params } = state.overlayParams ?? {}
      return { ...state, overlayParams: action.pinned ? { ...params, pinned: true } : params }
    }
    case 'back': {
      // Порядок закрытия -- сверху вниз по слоям: шит лежит поверх оверлея.
      if (state.sheet) return state.sheetBusy ? state : { ...state, sheet: null }
      if (navPinned(state) || fleetIsHome(state)) return state
      if (!state.overlay) return state
      // returnParams -- параметры слоя, куда возвращаемся: экран сервера
      // возвращает на список, и списку нужен его собственный returnTo.
      const params = state.overlayParams
      const target = normalizeReturn(params?.returnTo ?? null)
      // Возврат на слой вкладки (из «Маршрутов» -- на экран VPN-туннеля):
      // слой рисует его вкладка, поэтому вкладка встаёт вместе с ним.
      if (TAB_LAYERS[target]) {
        const back = { ...withoutParams(state), tab: TAB_LAYERS[target], overlay: target }
        return params?.returnParams ? { ...back, overlayParams: params.returnParams } : back
      }
      if (target === PARK_TAB) return { ...withoutParams(state), tab: PARK_TAB, overlay: null }
      // Возврат во вкладку («Ход работы» из «Управления»): слоя 'manage' нет,
      // есть вкладка -- иначе «назад» оставил бы пустую основную область.
      if (OVERLAY_TABS[target] && state.routerID != null) {
        return withFocus({ ...withoutParams(state), tab: OVERLAY_TABS[target], overlay: null }, MANAGE_FOCUS[state.overlay] ?? null)
      }
      const next = { ...withoutParams(state), overlay: target }
      // Слой «Управления» (пакеты, сброс DNS, настройки агента) закрыт --
      // возвращаемся в его группу, а не в стену свёрнутых.
      if (!next.overlay && state.routerID != null && state.tab === 'manage') return withFocus(next, MANAGE_FOCUS[state.overlay] ?? null)
      return next.overlay && params?.returnParams ? { ...next, overlayParams: params.returnParams } : next
    }
    default:
      return state
  }
}

// visibleOverlay -- слой, который человек видит. На широкой раскладке список
// роутеров («fleet») -- это боковая колонка, а не крышка: состояние остаётся
// (сузил окно -- список на месте), но закрывать «назад» или Esc там нечего.
function visibleOverlay(state, { wide = false } = {}) {
  const overlay = state?.overlay ?? null
  return wide && overlay === 'fleet' ? null : overlay
}

export function backButtonVisible(state, opts) {
  if (state?.sheet) return true
  const overlay = visibleOverlay(state, opts)
  return Boolean(overlay) && !navPinned(state) && !fleetIsHome(state)
}

// escapeAction -- что делает Esc. Лист подтверждения закрывает себя сам: он
// знает, идёт ли уже команда (тогда Esc не должен обрывать наблюдение).
export function escapeAction(state, opts) {
  if (state?.sheet) return null
  const overlay = visibleOverlay(state, opts)
  return overlay && !navPinned(state) && !fleetIsHome(state) ? { type: 'back' } : null
}

// awg3ListParams -- параметры списка «Серверы» (selfhosted) для экрана или формы
// панели: цепочка returnParams до слоя selfhosted. Нужна после удаления
// панели -- возвращаться на её экран уже некуда.
export function awg3ListParams(params) {
  let p = params
  for (let i = 0; i < 4 && p; i++) {
    if (p.returnTo === 'selfhosted') return p.returnParams ?? { returnTo: null }
    p = p.returnParams
  }
  return { returnTo: null }
}
