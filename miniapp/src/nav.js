// Навигация мини-аппа -- один чистый редьюсер, а не россыпь useState по
// компонентам. Причина: слоёв стало четыре (таб, оверлей, шит и выбранный
// роутер), а кнопка "назад" у Telegram одна, и решать, что она закрывает,
// должно одно место.
export const TABS = ['router', 'tunnels', 'diag', 'events']

// Таб "Маршруты" стал табом "Туннели": маршруты уехали внутрь туннеля, потому
// что оператор сначала спрашивает "какой VPN-туннель поднят", и только потом --
// "что через него идёт". Прежнее имя остаётся псевдонимом не из вежливости:
// deep-link из уже отправленных тревог живёт в переписке Telegram месяцами,
// и открыть по нему не тот экран молча было бы хуже, чем не открыть вовсе.
const TAB_ALIASES = { routes: 'tunnels' }

export function normalizeTab(tab) {
  return TAB_ALIASES[tab] ?? tab
}

// Оверлеи, которые можно открыть по адресу. Все они -- слои над выбранным
// роутером; список роутеров («fleet») сюда не входит: это выбор, а не место.
// Прежде ссылка открывала только настройки (кнопка «Панель роутера» в
// тревоге); веб-управлению нужны обновление страницы и закладки на любой слой.
export const OPEN_OVERLAYS = ['settings', 'admin', 'routes', 'agentcfg', 'dnsreset']

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
      return action.state ?? state
    case 'tab': {
      const tab = normalizeTab(action.tab)
      if (!TABS.includes(tab)) return state
      // Вкладки широкой раскладки видны и над открытым оверлеем: нажатие на
      // вкладку -- это уход со слоя, а не смена вкладки под ним.
      if (action.closeOverlay) return { ...state, tab, overlay: null, sheet: null }
      return { ...state, tab }
    }
    case 'router':
      return { ...state, routerID: action.id, tab: 'router', overlay: null, sheet: null }
    case 'overlay':
      return { ...state, overlay: action.overlay ?? null }
    case 'sheet': {
      // sheetSeq -- номер экземпляра листа, ключ его компонента. Новый лист
      // поверх открытого (без закрытия) обязан смонтироваться заново:
      // иначе набранное в первом -- имя, пароль root -- переехало бы во
      // второй, чужой роутер. Тот же объект листа номер не меняет.
      const sheet = action.sheet ?? null
      if (sheet && sheet !== state.sheet) return { ...state, sheet, sheetSeq: (state.sheetSeq ?? 0) + 1 }
      return { ...state, sheet }
    }
    case 'back':
      // Порядок закрытия -- сверху вниз по слоям: шит лежит поверх оверлея.
      if (state.sheet) return { ...state, sheet: null }
      if (state.overlay) return { ...state, overlay: null }
      return state
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
  return Boolean(state.sheet || visibleOverlay(state, opts))
}

// escapeAction -- что делает Esc. Лист подтверждения закрывает себя сам: он
// знает, идёт ли уже команда (тогда Esc не должен обрывать наблюдение).
export function escapeAction(state, opts) {
  if (state?.sheet) return null
  return visibleOverlay(state, opts) ? { type: 'back' } : null
}
