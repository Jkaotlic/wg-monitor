import { describe, it, expect } from 'vitest'
import { initialNav, navReducer, backButtonVisible, TABS, tabLabel, deepLinkOverlay, OPEN_OVERLAYS, FLEET_OVERLAYS, URL_FLEET_OVERLAYS, normalizeTab, escapeAction, navPinned } from '../src/nav.js'

describe('initialNav', () => {
  it('открывает роутер из deep-link', () => {
    const s = initialNav({ routerIDs: [7, 9], deepLinkID: 9 })
    expect(s.routerID).toBe(9)
    expect(s.overlay).toBe(null)
  })

  it('единственный доступный роутер открывается без промежуточного списка', () => {
    expect(initialNav({ routerIDs: [42], deepLinkID: null }).routerID).toBe(42)
  })

  it('при нескольких роутерах и без deep-link открывает список', () => {
    const s = initialNav({ routerIDs: [1, 2], deepLinkID: null })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe('fleet')
  })

  it('deep-link на недоступный роутер не выбирает его молча', () => {
    // Доступ проверяет сервер, но и клиент не должен делать вид, что
    // чужой роутер открыт: список честнее, чем экран с 404 внутри.
    const s = initialNav({ routerIDs: [1, 2], deepLinkID: 99 })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe('fleet')
  })

  it('без доступа к роутерам не открывает список', () => {
    const s = initialNav({ routerIDs: [], deepLinkID: null })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe(null)
  })

  it('всегда стартует с таба роутера', () => {
    expect(initialNav({ routerIDs: [1], deepLinkID: null }).tab).toBe('router')
  })
})

describe('navReducer', () => {
  const base = initialNav({ routerIDs: [1], deepLinkID: null })

  it('смена роутера возвращает на таб роутера и закрывает оверлей', () => {
    const s = navReducer({ ...base, tab: 'events', overlay: 'fleet' }, { type: 'router', id: 5 })
    expect(s).toMatchObject({ routerID: 5, tab: 'router', overlay: null })
  })

  it('back закрывает шит раньше оверлея', () => {
    const open = { ...base, overlay: 'admin', sheet: { title: 'Перезапустить туннель' } }
    const afterFirst = navReducer(open, { type: 'back' })
    expect(afterFirst.sheet).toBe(null)
    expect(afterFirst.overlay).toBe('admin')
    expect(navReducer(afterFirst, { type: 'back' }).overlay).toBe(null)
  })

  it('back на корневом экране ничего не меняет', () => {
    expect(navReducer(base, { type: 'back' })).toEqual(base)
  })

  it('неизвестный таб игнорируется', () => {
    expect(navReducer(base, { type: 'tab', tab: 'нечто' }).tab).toBe('router')
  })

  it('смена таба не трогает выбранный роутер', () => {
    const s = navReducer({ ...base, routerID: 3 }, { type: 'tab', tab: 'events' })
    expect(s).toMatchObject({ routerID: 3, tab: 'events' })
  })

  it('шит обновляется на месте -- ход выполнения виден в том же слое', () => {
    const running = navReducer({ ...base, sheet: { title: 'x', busy: false } }, {
      type: 'sheet',
      sheet: { title: 'x', busy: true },
    })
    expect(running.sheet.busy).toBe(true)
  })
})

describe('navReducer: init', () => {
  it('подставляет состояние, посчитанное после загрузки списка роутеров', () => {
    // Список приходит с сервера уже после первого рендера, поэтому стартовое
    // состояние считается дважды: пустым при монтировании и настоящим здесь.
    const s = navReducer(initialNav({ routerIDs: [], deepLinkID: null }), {
      type: 'init',
      state: initialNav({ routerIDs: [4], deepLinkID: 4 }),
    })
    expect(s).toMatchObject({ routerID: 4, tab: 'router', overlay: null })
  })

  it('init без состояния ничего не ломает', () => {
    const base = initialNav({ routerIDs: [1], deepLinkID: null })
    expect(navReducer(base, { type: 'init' })).toEqual(base)
  })
})

describe('backButtonVisible', () => {
  const base = initialNav({ routerIDs: [1], deepLinkID: null })
  it('скрыта на корневом экране', () => {
    expect(backButtonVisible(base)).toBe(false)
  })
  it('видна при открытом оверлее', () => {
    expect(backButtonVisible({ ...base, overlay: 'fleet' })).toBe(true)
  })
  it('видна при открытом шите', () => {
    expect(backButtonVisible({ ...base, sheet: { title: 'x' } })).toBe(true)
  })
})

// Таб "Маршруты" уступил место табу "Туннели": маршруты уехали внутрь
// туннеля, потому что оператор спрашивает "какая линия поднята", а уже
// потом -- "что через неё идёт".
describe('переименование таба маршрутов в туннели', () => {
  it('в списке табов есть tunnels и нет routes', () => {
    expect(TABS).toEqual(['router', 'tunnels', 'diag', 'events'])
  })

  it('переключение на tunnels работает', () => {
    const s = navReducer({ tab: 'router' }, { type: 'tab', tab: 'tunnels' })
    expect(s.tab).toBe('tunnels')
  })

  // Deep-link из старой тревоги ведёт на routes. Молча игнорировать его
  // значило бы открыть не тот экран и не сказать об этом: ссылка живёт в
  // уже отправленных сообщениях Telegram и будет приходить ещё месяцами.
  it('старый deep-link на routes открывает tunnels', () => {
    const s = navReducer({ tab: 'router' }, { type: 'tab', tab: 'routes' })
    expect(s.tab).toBe('tunnels')
  })

  it('незнакомый таб по-прежнему игнорируется', () => {
    const s = navReducer({ tab: 'router' }, { type: 'tab', tab: 'нечто' })
    expect(s.tab).toBe('router')
  })
})

// Ключи вкладок НЕ меняются: deep-link из уже отправленных тревог живёт в
// переписке месяцами, и открыть по нему не тот экран было бы хуже, чем не
// открыть вовсе. Меняются только подписи -- слова для человека.
describe('подписи вкладок', () => {
  it('человеческие, а ключи прежние', () => {
    expect(TABS).toEqual(['router', 'tunnels', 'diag', 'events'])
    expect(tabLabel('router')).toBe('Сейчас')
    expect(tabLabel('tunnels')).toBe('VPN-туннели')
    expect(tabLabel('diag')).toBe('Проверки')
    expect(tabLabel('events')).toBe('Что было')
  })

  it('незнакомый ключ не ломает вёрстку', () => {
    expect(tabLabel('нечто')).toBe('нечто')
  })
})

// Кнопка «Панель роутера» из бота ведёт сразу в настройки роутера, а не на
// главный экран, где настройки пришлось бы искать.
describe('deepLinkOverlay', () => {
  it('открывает любой оверлей роутера, но только вместе с роутером', () => {
    for (const o of ['settings', 'admin', 'routes', 'agentcfg', 'dnsreset', 'agentconn', 'packages', 'cabinet']) {
      expect(deepLinkOverlay(`?router=7&open=${o}`, { routerID: 7 })).toBe(o)
      expect(deepLinkOverlay(`?router=7&open=${o}`, { routerID: null })).toBe(null)
    }
    expect(OPEN_OVERLAYS).toEqual(['settings', 'admin', 'routes', 'agentcfg', 'dnsreset', 'agentconn', 'packages', 'cabinet'])
  })

  it('без open и с неизвестным open -- ничего', () => {
    expect(deepLinkOverlay('?router=7', { routerID: 7 })).toBe(null)
    expect(deepLinkOverlay('?router=7&open=fleet', { routerID: 7 })).toBe(null)
    expect(deepLinkOverlay('?router=7&open=rm-rf', { routerID: 7 })).toBe(null)
    for (const o of FLEET_OVERLAYS) expect(deepLinkOverlay(`?router=7&open=${o}`, { routerID: 7 })).toBe(null)
  })
})

describe('вкладка с закрытием оверлея', () => {
  it('closeOverlay закрывает оверлей и лист', () => {
    const s = { routerID: 1, tab: 'router', overlay: 'settings', sheet: { title: 'x' } }
    expect(navReducer(s, { type: 'tab', tab: 'diag', closeOverlay: true })).toEqual({ routerID: 1, tab: 'diag', overlay: null, sheet: null })
  })

  it('без closeOverlay поведение прежнее', () => {
    const s = { routerID: 1, tab: 'router', overlay: 'settings', sheet: null }
    expect(navReducer(s, { type: 'tab', tab: 'diag' }).overlay).toBe('settings')
  })

  it('неизвестная вкладка не закрывает и оверлей', () => {
    const s = { routerID: 1, tab: 'router', overlay: 'settings', sheet: null }
    expect(navReducer(s, { type: 'tab', tab: 'nope', closeOverlay: true })).toBe(s)
  })

  it('normalizeTab знает псевдоним routes', () => {
    expect(normalizeTab('routes')).toBe('tunnels')
    expect(normalizeTab('diag')).toBe('diag')
  })
})

describe('escapeAction', () => {
  it('Esc закрывает оверлей, лист оставляет самому листу', () => {
    expect(escapeAction({ overlay: 'settings', sheet: null })).toEqual({ type: 'back' })
    expect(escapeAction({ overlay: 'settings', sheet: { title: 'x' } })).toBe(null)
    expect(escapeAction({ overlay: null, sheet: null })).toBe(null)
  })
})

// На широкой раскладке список роутеров -- боковая колонка, слой «fleet»
// невидим. Кнопка «назад» и Esc над ним закрывали бы то, чего не видно.
describe('невидимый список роутеров на широком экране', () => {
  it('«назад» не показывается, Esc ничего не делает', () => {
    const s = { routerID: null, tab: 'router', overlay: 'fleet', sheet: null }
    expect(backButtonVisible(s, { wide: true })).toBe(false)
    expect(escapeAction(s, { wide: true })).toBe(null)
  })

  it('на телефоне -- как было', () => {
    const s = { routerID: null, tab: 'router', overlay: 'fleet', sheet: null }
    expect(backButtonVisible(s)).toBe(true)
    expect(escapeAction(s)).toEqual({ type: 'back' })
  })

  it('прочие слои и лист на широком экране видны', () => {
    expect(backButtonVisible({ overlay: 'settings', sheet: null }, { wide: true })).toBe(true)
    expect(backButtonVisible({ overlay: 'fleet', sheet: { title: 'x' } }, { wide: true })).toBe(true)
    expect(escapeAction({ overlay: 'admin', sheet: null }, { wide: true })).toEqual({ type: 'back' })
  })
})
// Слои всего парка: мастер «Добавить роутер», «Ход работы», ожидание раскатки
// бэкенда. Открываются и без роутера и знают, куда вернуться.
describe('слои парка', () => {
  const base = { routerID: null, tab: 'router', overlay: null, sheet: null }

  it('список слоёв парка', () => {
    expect(FLEET_OVERLAYS).toEqual(['provision', 'job', 'backenddeploy', 'selfhosted', 'selfhostedinst'])
  })

  it('параметры слоя кладутся рядом с ним и уходят вместе с ним', () => {
    const s = navReducer(base, { type: 'overlay', overlay: 'job', params: { jobId: 'j1', title: 'Установка', returnTo: 'admin' } })
    expect(s).toEqual({ ...base, overlay: 'job', overlayParams: { jobId: 'j1', title: 'Установка', returnTo: 'admin' } })
    const plain = navReducer(s, { type: 'overlay', overlay: 'admin' })
    expect(plain).toEqual({ ...base, overlay: 'admin' })
    expect('overlayParams' in plain).toBe(false)
  })

  it('«назад» со слоя парка -- туда, откуда пришли', () => {
    const s = { ...base, routerID: 3, overlay: 'provision', overlayParams: { returnTo: 'admin' } }
    const back = navReducer(s, { type: 'back' })
    expect(back).toEqual({ ...base, routerID: 3, overlay: 'admin' })
    const fromHome = navReducer({ ...base, overlay: 'job', overlayParams: { jobId: 'j', returnTo: null } }, { type: 'back' })
    expect(fromHome).toEqual(base)
  })

  it('ожидание раскатки не закрывается «назад» и Esc', () => {
    const s = { ...base, overlay: 'backenddeploy', overlayParams: { targetVersion: 'v0.36.0', returnTo: null } }
    expect(navReducer(s, { type: 'back' })).toBe(s)
    expect(backButtonVisible(s)).toBe(false)
    expect(backButtonVisible(s, { wide: true })).toBe(false)
    expect(escapeAction(s)).toBe(null)
  })

  it('прочие слои парка -- «назад» видна, Esc закрывает', () => {
    for (const overlay of ['provision', 'job']) {
      const s = { ...base, overlay, overlayParams: { returnTo: null } }
      expect(backButtonVisible(s)).toBe(true)
      expect(escapeAction(s, { wide: true })).toEqual({ type: 'back' })
    }
  })

  it('смена роутера и вкладка с закрытием стирают параметры', () => {
    const s = { ...base, routerID: 1, overlay: 'job', overlayParams: { jobId: 'j' } }
    expect('overlayParams' in navReducer(s, { type: 'router', id: 2 })).toBe(false)
    expect(navReducer(s, { type: 'tab', tab: 'diag', closeOverlay: true })).toEqual({ routerID: 1, tab: 'diag', overlay: null, sheet: null })
  })
})

// Закреплённый слой: ожидание раскатки всегда, мастер -- пока идёт отправка.
// Уйти с него нельзя ни «назад», ни Esc, ни выбором роутера или вкладки, ни
// «назад» браузера; выпускает только действие с unpin.
describe('закреплённый слой', () => {
  const base = { routerID: 3, tab: 'router', overlay: null, sheet: null }
  const deploy = { ...base, overlay: 'backenddeploy', overlayParams: { targetVersion: 'v0.36.0', returnTo: 'admin' } }
  const busyWizard = { ...base, overlay: 'provision', overlayParams: { returnTo: 'admin', pinned: true } }

  it('navPinned: раскатка всегда, мастер -- только с pinned', () => {
    expect(navPinned(deploy)).toBe(true)
    expect(navPinned(busyWizard)).toBe(true)
    expect(navPinned({ ...base, overlay: 'provision', overlayParams: { returnTo: 'admin' } })).toBe(false)
    expect(navPinned(base)).toBe(false)
  })

  it('pin/unpin мастера кладёт флаг в параметры слоя', () => {
    const s = navReducer({ ...base, overlay: 'provision', overlayParams: { returnTo: 'admin' } }, { type: 'pin', pinned: true })
    expect(s.overlayParams).toEqual({ returnTo: 'admin', pinned: true })
    expect(navReducer(s, { type: 'pin', pinned: false }).overlayParams).toEqual({ returnTo: 'admin' })
    expect(navReducer(base, { type: 'pin', pinned: true })).toBe(base)
  })

  for (const [name, s] of [['раскатка', deploy], ['мастер в работе', busyWizard]]) {
    it(`${name}: назад, Esc, роутер, вкладка, чужой слой и popstate -- без изменений`, () => {
      expect(navReducer(s, { type: 'back' })).toBe(s)
      expect(backButtonVisible(s)).toBe(false)
      expect(escapeAction(s, { wide: true })).toBe(null)
      expect(navReducer(s, { type: 'router', id: 9 })).toBe(s)
      expect(navReducer(s, { type: 'tab', tab: 'diag', closeOverlay: true })).toBe(s)
      expect(navReducer(s, { type: 'tab', tab: 'diag' })).toBe(s)
      expect(navReducer(s, { type: 'overlay', overlay: 'admin' })).toBe(s)
      expect(navReducer(s, { type: 'init', state: base, source: 'popstate' })).toBe(s)
    })
  }

  it('действие с unpin выпускает: мастер -- в «Ход работы», раскатка -- «Вернуться»', () => {
    const job = navReducer(busyWizard, { type: 'overlay', overlay: 'job', params: { jobId: 'j1', title: 't', returnTo: 'admin' }, unpin: true })
    expect(job).toEqual({ ...base, overlay: 'job', overlayParams: { jobId: 'j1', title: 't', returnTo: 'admin' } })
    expect(navReducer(deploy, { type: 'overlay', overlay: 'admin', unpin: true })).toEqual({ ...base, overlay: 'admin' })
  })
})

// Кабинеты и свои серверы (цикл 3 «бот без слеш-команд»).
describe('кабинет и свои серверы', () => {
  const base = { routerID: null, tab: 'router', overlay: null, sheet: null }

  it('слой с адресом среди слоёв парка -- только список серверов', () => {
    expect(URL_FLEET_OVERLAYS).toEqual(['selfhosted'])
    for (const o of URL_FLEET_OVERLAYS) expect(FLEET_OVERLAYS).toContain(o)
  })

  it('кабинет открывается по адресу вместе с роутером', () => {
    expect(deepLinkOverlay('?router=7&open=cabinet', { routerID: 7 })).toBe('cabinet')
    expect(deepLinkOverlay('?router=7&open=cabinet', { routerID: null })).toBe(null)
  })

  it('«назад» с экрана сервера -- на список с его собственным возвратом', () => {
    const s = {
      ...base,
      routerID: 3,
      overlay: 'selfhostedinst',
      overlayParams: { instanceId: 'ams', returnTo: 'selfhosted', returnParams: { returnTo: 'admin' } },
    }
    const list = navReducer(s, { type: 'back' })
    expect(list).toEqual({ ...base, routerID: 3, overlay: 'selfhosted', overlayParams: { returnTo: 'admin' } })
    expect(navReducer(list, { type: 'back' })).toEqual({ ...base, routerID: 3, overlay: 'admin' })
  })

  it('returnParams без returnTo не создают слой из ничего', () => {
    const s = { ...base, overlay: 'selfhosted', overlayParams: { returnTo: null, returnParams: { returnTo: 'admin' } } }
    const back = navReducer(s, { type: 'back' })
    expect(back).toEqual(base)
    expect('overlayParams' in back).toBe(false)
  })

  it('лист поверх экрана сервера закрывается первым', () => {
    const s = { ...base, overlay: 'selfhostedinst', overlayParams: { instanceId: 'ams', returnTo: 'selfhosted' }, sheet: { title: 'Удалить?' } }
    expect(navReducer(s, { type: 'back' })).toEqual({ ...s, sheet: null })
  })
})

// «Назад» Telegram не закрывает занятый лист: запрос уже ушёл, и человек
// остался бы без ответа на то, что сделал (M8 ревью цикла 3).
describe('занятый лист', () => {
  const base = { routerID: 1, tab: 'router', overlay: 'cabinet', sheet: null }
  const sheet = { title: 'Удалить?' }

  it('sheetBusy помечает открытый лист; без листа -- ничего', () => {
    expect(navReducer(base, { type: 'sheetBusy', busy: true })).toBe(base)
    const open = navReducer(base, { type: 'sheet', sheet })
    const busy = navReducer(open, { type: 'sheetBusy', busy: true })
    expect(busy.sheetBusy).toBe(true)
    const free = navReducer(busy, { type: 'sheetBusy', busy: false })
    expect('sheetBusy' in free).toBe(false)
  })

  it('«назад» не закрывает занятый лист и не трогает слой под ним', () => {
    const busy = navReducer(navReducer(base, { type: 'sheet', sheet }), { type: 'sheetBusy', busy: true })
    expect(navReducer(busy, { type: 'back' })).toBe(busy)
    const free = navReducer(busy, { type: 'sheetBusy', busy: false })
    expect(navReducer(free, { type: 'back' }).sheet).toBe(null)
  })

  it('закрытие листа самим листом снимает занятость', () => {
    const busy = navReducer(navReducer(base, { type: 'sheet', sheet }), { type: 'sheetBusy', busy: true })
    const closed = navReducer(busy, { type: 'sheet', sheet: null })
    expect(closed.sheet).toBe(null)
    expect('sheetBusy' in closed).toBe(false)
    expect(navReducer(closed, { type: 'back' }).overlay).toBe(null)
  })
})

