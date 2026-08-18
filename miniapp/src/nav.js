// Навигация мини-аппа -- один чистый редьюсер, а не россыпь useState по
// компонентам. Причина: слоёв стало четыре (таб, оверлей, шит и выбранный
// роутер), а кнопка "назад" у Telegram одна, и решать, что она закрывает,
// должно одно место.
export const TABS = ['router', 'routes', 'diag', 'events']

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

export function navReducer(state, action) {
  switch (action.type) {
    // Список роутеров приходит с сервера уже после первого рендера, поэтому
    // стартовое состояние подставляется отдельным действием, а не считается
    // в useReducer -- иначе выбор "открыть роутер или показать список"
    // пришлось бы делать до того, как известно, что доступно.
    case 'init':
      return action.state ?? state
    case 'tab':
      return TABS.includes(action.tab) ? { ...state, tab: action.tab } : state
    case 'router':
      return { ...state, routerID: action.id, tab: 'router', overlay: null, sheet: null }
    case 'overlay':
      return { ...state, overlay: action.overlay ?? null }
    case 'sheet':
      return { ...state, sheet: action.sheet ?? null }
    case 'back':
      // Порядок закрытия -- сверху вниз по слоям: шит лежит поверх оверлея.
      if (state.sheet) return { ...state, sheet: null }
      if (state.overlay) return { ...state, overlay: null }
      return state
    default:
      return state
  }
}

export function backButtonVisible(state) {
  return Boolean(state.sheet || state.overlay)
}
