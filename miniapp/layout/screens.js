// Обход приёмки раскладки (спека §9): каждая вкладка и каждый слой × роли.
// Подписи -- словарь v0.52. Экран обязан открыться: пропусков нет, не открылся
// -- провал прогона. Чего в песочнице «не бывает», то в ней засеяно (роль none,
// остановленный HydraRoute Neo, версия для раскатки бэкенда).
//
// Роутер экрана: router -- для админа (выбирает из парка); остальные роли --
// свой роутер по DEFAULT_ROUTER, если экран не переопределил его в routers.
// Какой роутер снят на самом деле, run.mjs пишет в запись; не тот -- провал.
export const ROLES = ['admin', 'owner1', 'owner3', 'operator', 'issuer', 'none']
// «Последний выпуск» песочницы (флаг -latest): Парк предлагает раскатку до него.
export const SANDBOX_LATEST = 'v0.34.0'
// Роутер с остановленным HydraRoute Neo (флаг песочницы -hrneo-stopped, он же по умолчанию).
const HRNEO_STOPPED = 'дача-северная'
export const WIDTHS = [360, 390, 1024, 1440]

export const DEFAULT_ROUTER = {
  owner1: 'sandbox-home',
  // Длинное имя -- текущий роутер владельца трёх (чип полосы и шапка).
  owner3: 'router4car4new',
  operator: 'sandbox-home',
  issuer: 'sandbox-work',
}

// Все, у кого есть роутер (роль none -- человек без доступа, у него один экран).
const ALL = ROLES.filter((r) => r !== 'none')
const OWNERS = ['admin', 'owner1', 'owner3']
const TUNNELS = { tab: 'VPN-туннели' }
const NEW = [TUNNELS, { click: 'Новый VPN-туннель' }]
const CABINET = [...NEW, { sheetChoice: 'Amnezia' }]
const PARK = { tab: 'Парк' }
const MANAGE = [{ tab: 'Настройки' }, { expandAll: true }]
const ROUTES = [TUNNELS, { click: 'Маршруты: куда идёт трафик' }]
const AWG3 = [PARK, { click: 'Панели VPN-серверов' }]

// Порядок: экраны беды -- первыми, пока тревога песочницы свежая (стареет за
// 5 мин; run.mjs поднимает песочницу заново на каждую ширину).
export const SCREENS = [
  { id: 'repair', roles: ['owner3', 'admin'], router: 'sandbox-broken', routers: { owner3: 'sandbox-broken' }, steps: [{ tab: 'Роутер' }, { click: 'Починить' }] },
  { id: 'silence', roles: ['owner3', 'admin'], router: 'sandbox-broken', routers: { owner3: 'sandbox-broken' }, steps: [{ tab: 'Роутер' }, { click: 'Не беспокоить' }] },
  { id: 'router', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Роутер' }] },
  { id: 'tunnels', roles: ALL, router: 'sandbox-home', steps: [TUNNELS] },
  { id: 'new-tunnel-sheet', roles: ALL, router: 'sandbox-home', steps: NEW },
  { id: 'cabinet', roles: ALL, router: 'sandbox-home', steps: CABINET },
  { id: 'cabinet-hidemy', roles: ALL, router: 'sandbox-home', steps: [...CABINET, { segment: 'HideMy' }] },
  { id: 'cabinet-selfhosted', roles: ['admin'], router: 'sandbox-home', steps: [...CABINET, { segment: 'Свой сервер' }] },
  { id: 'cabinet-issue', roles: ALL, router: 'sandbox-home', steps: [...CABINET, { first: '.cabinet-option-main:not([disabled])' }] },
  // Допущенный видит и запертую панель old: «сообщите администратору» (спека §8).
  // Фраза -- своя у роли и ищется в верхнем слое (run.mjs), а не по всей странице:
  // слой под ним её не подменит. Тексты -- awg3Panel.js ISSUER_PANEL_DOWN и CabinetAwg3.jsx.
  { id: 'cabinet-awg3', roles: ['admin', 'issuer'], router: 'sandbox-work', steps: [...NEW, { sheetChoice: 'Панель VPN-сервера' }, { expect: { issuer: 'Панель VPN-сервера сейчас недоступна, сообщите администратору', admin: 'Панель сейчас не отвечает' } }] },
  { id: 'confimport', roles: ALL, router: 'sandbox-home', steps: [...NEW, { sheetChoice: 'Загрузить .conf' }] },
  { id: 'tunnel', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { rowIn: 'Все VPN-туннели' }] },
  { id: 'tunnel-delete', roles: OWNERS, router: 'sandbox-home', steps: [TUNNELS, { tunnelWith: 'Удалить VPN-туннель' }, { click: 'Удалить VPN-туннель' }] },
  { id: 'replace', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { tunnelWith: 'Заменить конфиг' }, { click: 'Заменить конфиг' }] },
  { id: 'routes', roles: ALL, router: 'sandbox-home', steps: ROUTES },
  { id: 'routeadd', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Добавить сайт или адрес' }] },
  { id: 'routepick', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Перенести' }] },
  { id: 'make-default', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Сделать главным' }] },
  { id: 'hrneo-stop', roles: OWNERS, router: 'sandbox-home', steps: [...ROUTES, { click: 'Остановить' }] },
  // «Запустить» -- только когда HydraRoute Neo остановлен: у админа и владельца
  // трёх это засеянный роутер. У владельца одного роутер единственный и нужен
  // запущенным для hrneo-stop -- его экран ниже, последним: он сам останавливает.
  { id: 'hrneo-start', roles: ['admin', 'owner3'], router: HRNEO_STOPPED, routers: { owner3: HRNEO_STOPPED }, steps: [...ROUTES, { waitButton: 'Запустить' }, { click: 'Запустить' }, { expect: 'Запустить HydraRoute.Neo\\?' }] },
  { id: 'diag-now', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }] },
  { id: 'diag-history', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }, { segment: 'Что было' }] },
  { id: 'manage', roles: ALL, router: 'sandbox-home', steps: MANAGE },
  { id: 'reboot', roles: OWNERS, router: 'sandbox-home', steps: [...MANAGE, { click: 'Перезагрузить роутер' }, { fill: '$router' }] },
  { id: 'agentcfg', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть настройки агента' }] },
  { id: 'agentconn', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть подключение агента' }] },
  { id: 'dnsreset', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть сброс DNS' }] },
  { id: 'packages', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть пакеты по расписанию' }] },
  // Владелец трёх роутеров: ещё один длинный роутер текущим.
  // Полоса «Мои роутеры» в прокрученном состоянии (только телефон: на широкой
  // раскладке полосы нет). strip-effect: после перебора чипов длинное имя
  // снова текущее -- красный чип слева, начало имени правее него.
  { id: 'strip-effect', roles: ['owner3'], maxWidth: 800, router: 'дача-северная', routers: { owner3: 'дача-северная' }, steps: [{ tab: 'Роутер' }, { stripScroll: 'end' }, { stripPick: 'router4car4new' }, { stripPick: 'дача-северная' }, { stripExpect: 'effect' }] },
  // strip-user: человек сам прокрутил полосу до конца -- красный чип прилип к левому полю.
  { id: 'strip-user', roles: ['owner3'], maxWidth: 800, router: 'дача-северная', routers: { owner3: 'дача-северная' }, steps: [{ tab: 'Роутер' }, { stripScroll: 'end' }, { stripExpect: 'pinned' }] },
  { id: 'long-router', roles: ['owner3'], router: 'дача-северная', routers: { owner3: 'дача-северная' }, steps: [{ tab: 'Роутер' }] },
  { id: 'long-tunnels', roles: ['owner3'], router: 'дача-северная', routers: { owner3: 'дача-северная' }, steps: [TUNNELS] },
  { id: 'long-manage', roles: ['owner3'], router: 'дача-северная', routers: { owner3: 'дача-северная' }, steps: MANAGE },
  { id: 'fleet-list', roles: ['admin'], steps: [PARK, { headerPick: true }] },
  { id: 'park', roles: ['admin'], steps: [PARK] },
  { id: 'park-more', roles: ['admin'], steps: [PARK, { clickAll: 'Ещё' }] },
  { id: 'park-recheck-all', roles: ['admin'], steps: [PARK, { click: 'Проверить заново все' }] },
  { id: 'park-doctor-all', roles: ['admin'], steps: [PARK, { click: 'Осмотреть все' }] },
  { id: 'park-versions', roles: ['admin'], steps: [PARK, { click: 'Сверить версии' }] },
  { id: 'revive', roles: ['admin'], steps: [PARK, { clickAll: 'Ещё' }, { click: 'Оживить агент' }] },
  { id: 'selfhosted-vps', roles: ['admin'], steps: [PARK, { click: 'Свои VPS' }] },
  { id: 'selfhosted-inst', roles: ['admin'], steps: [PARK, { click: 'Свои VPS' }, { first: '.overlay .list-row-btn' }] },
  { id: 'awg3-list', roles: ['admin'], steps: AWG3 },
  { id: 'awg3panel', roles: ['admin'], steps: [...AWG3, { click: 'Main (Амстердам)' }, { expect: 'Кто может выпускать конфиги' }] },
  { id: 'awg3panel-readonly', roles: ['admin'], steps: [...AWG3, { click: 'nl2 (полигон)' }, { expect: 'Кто может выпускать конфиги' }] },
  { id: 'awg3panel-paused', roles: ['admin'], steps: [...AWG3, { click: 'Бан 15 минут' }] },
  { id: 'awg3panel-down', roles: ['admin'], steps: [...AWG3, { click: 'Старый пароль' }] },
  { id: 'awg3form', roles: ['admin'], steps: [...AWG3, { click: 'Main (Амстердам)' }, { click: 'Настройки панели' }] },
  { id: 'awg3add', roles: ['admin'], steps: [...AWG3, { click: 'Добавить панель' }] },
  { id: 'provision', roles: ['admin'], steps: [PARK, { click: 'Добавить роутер' }] },
  // Человек без доступа: один экран, до и после «Проверить снова».
  { id: 'noaccess', roles: ['none'], steps: [{ expect: 'Роутер ещё не привязан' }] },
  { id: 'noaccess-checked', roles: ['none'], steps: [{ click: 'Проверить снова' }, { waitText: 'Пока ничего не изменилось' }] },
  // Владелец одного роутера: останавливает HydraRoute Neo настоящей командой и
  // открывает лист «Запустить». Последним: дальше роутер остаётся с остановленным.
  { id: 'hrneo-start', roles: ['owner1'], router: 'sandbox-home', steps: [...ROUTES, { waitButton: 'Остановить' }, { click: 'Остановить' }, { click: 'Остановить' }, { waitButton: 'Закрыть' }, { click: 'Закрыть' }, { waitButton: 'Запустить' }, { click: 'Запустить' }, { expect: 'Запустить HydraRoute.Neo\\?' }] },
  // «Ход работы»: переустановка агента на настоящем движке заданий песочницы
  // (подменён только терминал роутера). Пароль -- любое слово: настоящих
  // секретов в песочнице нет. Снимается итог задания -- он устойчив.
  { id: 'job', roles: ['admin'], steps: [PARK, { clickAll: 'Ещё' }, { click: 'Переустановить агент' }, { fillSel: '#sheet-field-root_password', value: 'sandbox' }, { fillSel: '#sheet-confirm-input', value: '$confirm' }, { click: 'Переустановить' }, { waitText: 'Агент переустановлен и на связи', ms: 40000 }] },
  // Ожидание раскатки бэкенда: слой закреплён, песочница заявку принимает и
  // молчит (-backend-update ignore). После снимка -- перезагрузка страницы.
  { id: 'backenddeploy', roles: ['admin'], reload: true, steps: [PARK, { click: `Обновить бэкенд до ${SANDBOX_LATEST}` }, { fillSel: '#sheet-confirm-input', value: '$confirm' }, { click: 'Обновить бэкенд' }, { waitText: `Бэкенд обновляется до ${SANDBOX_LATEST}` }, { waitText: 'прежней версией' }] },
]

// Что обязан показать шаг expect этой роли: строка -- всем, объект -- по роли.
// Роль без фразы -- ошибка обхода, а не «подойдёт любая».
export function expectPattern(step, role) {
  if (typeof step.expect === 'string') return step.expect
  const p = step.expect?.[role]
  if (!p) throw new Error(`шаг expect без фразы для роли ${role}`)
  return p
}
