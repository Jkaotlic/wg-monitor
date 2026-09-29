// Навигация мини-аппа -- один чистый редьюсер, а не россыпь useState по
// компонентам. Причина: слоёв стало четыре (таб, оверлей, шит и выбранный
// роутер), а кнопка "назад" у Telegram одна, и решать, что она закрывает,
// должно одно место.
// «Управление» (v0.41) -- пятая вкладка вместо шестерёнки в шапке и строки
// «Администрирование» внизу «Сейчас»: настройки роутера и его обслуживание
// стали функцией для всех, а не спрятанным входом.
export const TABS = ['router', 'tunnels', 'diag', 'events', 'manage']

// «Парк» (v0.48) -- вкладка админа, первая в панели: весь парк, от
// выбранного роутера не зависит и открывается без него. Раньше он жил хвостом
// под «Моими роутерами», и список роутеров становился экраном инструментов.
// Кому она видна, решает оболочка по is_admin -- тот же признак, по которому
// Парк показывался под списком; редьюсер вкладку не прячет.
export const PARK_TAB = 'park'

// barTabs -- что в нижней панели. Админу с роутером -- Парк и пять вкладок
// роутера. Без роутера (главный экран -- список) вкладкам роутера показывать
// нечего: панель -- Парк и сам список ('fleet' -- не вкладка, а слой; его
// открывает оболочка). Остальным -- прежние пять, как было.
export function barTabs({ isAdmin = false, routerID = null } = {}) {
  if (!isAdmin) return TABS
  return routerID != null ? [PARK_TAB, ...TABS] : [PARK_TAB, 'fleet']
}

// Таб "Маршруты" стал табом "Туннели": маршруты уехали внутрь туннеля, потому
// что оператор сначала спрашивает "какой VPN-туннель поднят", и только потом --
// "что через него идёт". Прежнее имя остаётся псевдонимом не из вежливости:
// deep-link из уже отправленных тревог живёт в переписке Telegram месяцами,
// и открыть по нему не тот экран молча было бы хуже, чем не открыть вовсе.
const TAB_ALIASES = { routes: 'tunnels' }

export function normalizeTab(tab) {
  return TAB_ALIASES[tab] ?? tab
}

// Слои, ставшие вкладкой. Настройки (?open=settings) и «Обслуживание и
// доступы» (?open=admin) переехали во вкладку «Управление»; ссылки на них
// живут в уже отправленных уведомлениях месяцами и обязаны вести туда же.
// 'manage' -- возврат слоя («Ход работы» перенаправления) во вкладку.
export const OVERLAY_TABS = { settings: 'manage', admin: 'manage', manage: 'manage' }

// Группа «Управления», которую раскрыть, когда туда ведёт старая ссылка
// (?open=settings / ?open=admin) или возврат из слоя глубже вкладки (v0.50):
// группы свёрнуты, и без этого человек приходил бы в стену заголовков.
export const MANAGE_FOCUS = { settings: 'router', admin: 'repair', packages: 'repair', dnsreset: 'repair', agentcfg: 'settings', agentconn: 'settings' }

// Фокус -- как параметры слоя: ключ есть только когда он задан, чтобы
// прежние снимки навигации не меняли форму.
function withoutFocus(state) {
  if (!state || !('manageFocus' in state)) return state
  const { manageFocus: _drop, ...rest } = state
  return rest
}

function withFocus(state, focus) {
  return focus ? { ...state, manageFocus: focus } : state
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
// «Свои VPN-серверы» (selfhosted) и экран одного сервера (selfhostedinst) --
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
  return state?.overlay === 'fleet' && state?.routerID == null
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

// Подписи отделены от ключей намеренно. Ключ -- это адрес, по которому в
// приложение приходят deep-link'и из тревог, отправленных месяцы назад;
// подпись -- слова для человека. Менять их вместе значило бы ломать ссылки
// ради текста.
//
// Слова выбраны по вопросу, на который отвечает вкладка: «что сейчас», «через
// что ходит трафик», «что проверено», «что было». Прежние «Роутер», «Туннели»,
// «Диагностика» называли устройство и инструмент, а не ответ.
const TAB_LABELS = {
  router: 'Сейчас',
  tunnels: 'VPN-туннели',
  diag: 'Проверки',
  events: 'Что было',
  manage: 'Управление',
  park: 'Парк',
}

export function tabLabel(tab) {
  return TAB_LABELS[tab] ?? tab
}

// Подпись в нижней панели. Шесть вкладок на 360 px: «VPN-туннели» там --
// «Туннели», заголовок экрана и шапка широкого экрана остаются полными.
const BAR_LABELS = { tunnels: 'Туннели', fleet: 'Роутеры' }

export function barLabel(tab) {
  return BAR_LABELS[tab] ?? tabLabel(tab)
}

export function initialNav({ routerIDs = [], deepLinkID = null } = {}) {
  const state = { routerID: null, tab: 'router', overlay: null, sheet: null }
  // Deep-link с тревоги ведёт на конкретный роутер, но не обходит доступ:
  // сервер отдаст 404, а клиент не должен делать вид, что чужой роутер открыт.
  if (deepLinkID != null && routerIDs.includes(deepLinkID)) {
    state.routerID = deepLinkID
    return state
  }
  if (routerIDs.length === 1) {
    state.routerID = routerIDs[0]
    return state
  }
  // Пустой доступ -- отдельный экран, а не список из нуля строк.
  if (routerIDs.length > 1) state.overlay = 'fleet'
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
      // Вкладки широкой раскладки видны и над открытым оверлеем: нажатие на
      // вкладку -- это уход со слоя, а не смена вкладки под ним.
      if (action.closeOverlay) return withoutFocus({ ...withoutParams(state), tab, overlay: null, sheet: null })
      return withoutFocus({ ...state, tab })
    }
    case 'router': {
      if (navPinned(state)) return state
      // keepTab -- смена роутера из шапки (v0.50): вкладка роутера остаётся.
      // tab -- переход сразу на вкладку («Открыть VPN-туннели «ник»»).
      const wanted = action.tab ? normalizeTab(action.tab) : null
      const kept = action.keepTab && TABS.includes(state.tab) ? state.tab : null
      const tab = wanted && TABS.includes(wanted) ? wanted : kept ?? 'router'
      return withoutFocus({ ...withoutParams(state), routerID: action.id, tab, overlay: null, sheet: null })
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
      const next = { ...withoutParams(state), overlay }
      if (!overlay && state.routerID != null && state.tab === 'manage') return withFocus(next, MANAGE_FOCUS[state.overlay] ?? null)
      return overlay && action.params ? { ...next, overlayParams: action.params } : next
    }
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
      if (state.overlay !== 'provision') return state
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

// awg3ListParams -- параметры списка «Свои VPN-серверы» для экрана или формы
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
