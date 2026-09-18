// Навигация мини-аппа -- один чистый редьюсер, а не россыпь useState по
// компонентам. Причина: слоёв стало четыре (таб, оверлей, шит и выбранный
// роутер), а кнопка "назад" у Telegram одна, и решать, что она закрывает,
// должно одно место.
// «Управление» (v0.41) -- пятая вкладка вместо шестерёнки в шапке и строки
// «Администрирование» внизу «Сейчас»: настройки роутера и его обслуживание
// стали функцией для всех, а не спрятанным входом.
export const TABS = ['router', 'tunnels', 'diag', 'events', 'manage']

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
export const FLEET_OVERLAYS = ['provision', 'job', 'backenddeploy', 'selfhosted', 'selfhostedinst']

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
}

export function tabLabel(tab) {
  return TAB_LABELS[tab] ?? tab
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
      if (!TABS.includes(tab) || navPinned(state)) return state
      // Вкладки широкой раскладки видны и над открытым оверлеем: нажатие на
      // вкладку -- это уход со слоя, а не смена вкладки под ним.
      if (action.closeOverlay) return { ...withoutParams(state), tab, overlay: null, sheet: null }
      return { ...state, tab }
    }
    case 'router':
      if (navPinned(state)) return state
      return { ...withoutParams(state), routerID: action.id, tab: 'router', overlay: null, sheet: null }
    case 'overlay': {
      if (navPinned(state) && !action.unpin) return state
      const overlay = action.overlay ?? null
      if (OVERLAY_TABS[overlay] && state.routerID != null) {
        return { ...withoutParams(state), tab: OVERLAY_TABS[overlay], overlay: null, sheet: null }
      }
      const next = { ...withoutParams(state), overlay }
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
      if (navPinned(state)) return state
      if (!state.overlay) return state
      // returnParams -- параметры слоя, куда возвращаемся: экран сервера
      // возвращает на список, и списку нужен его собственный returnTo.
      const params = state.overlayParams
      const target = normalizeReturn(params?.returnTo ?? null)
      // Возврат во вкладку («Ход работы» из «Управления»): слоя 'manage' нет,
      // есть вкладка -- иначе «назад» оставил бы пустую основную область.
      if (OVERLAY_TABS[target] && state.routerID != null) {
        return { ...withoutParams(state), tab: OVERLAY_TABS[target], overlay: null }
      }
      const next = { ...withoutParams(state), overlay: target }
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
  return Boolean(overlay) && !navPinned(state)
}

// escapeAction -- что делает Esc. Лист подтверждения закрывает себя сам: он
// знает, идёт ли уже команда (тогда Esc не должен обрывать наблюдение).
export function escapeAction(state, opts) {
  if (state?.sheet) return null
  const overlay = visibleOverlay(state, opts)
  return overlay && !navPinned(state) ? { type: 'back' } : null
}
