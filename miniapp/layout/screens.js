// Обход приёмки раскладки (спека §9): каждая вкладка и каждый слой × роли.
// Подписи -- словарь v0.52. Экран обязан открыться: пропуск -- провал прогона
// (optional -- только там, где кнопки может не быть по данным песочницы, и
// такой пропуск попадает в отчёт отдельной строкой).
//
// Роутер экрана: router -- для админа (выбирает из парка); остальные роли --
// свой роутер по DEFAULT_ROUTER, если экран не переопределил его в routers.
// Какой роутер снят на самом деле, run.mjs пишет в запись; не тот -- провал.
export const ROLES = ['admin', 'owner1', 'owner3', 'operator', 'issuer']
export const WIDTHS = [360, 390, 1024, 1440]

export const DEFAULT_ROUTER = {
  owner1: 'sandbox-home',
  // Длинное имя -- текущий роутер владельца трёх (чип полосы и шапка).
  owner3: 'router4car4new',
  operator: 'sandbox-home',
  issuer: 'sandbox-work',
}

const ALL = ROLES
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
  { id: 'cabinet-awg3', roles: ['admin', 'issuer'], router: 'sandbox-work', steps: [...NEW, { sheetChoice: 'Панель VPN-сервера' }, { expect: 'сообщите администратору|не отвечает' }] },
  { id: 'confimport', roles: ALL, router: 'sandbox-home', steps: [...NEW, { sheetChoice: 'Загрузить .conf' }] },
  { id: 'tunnel', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { rowIn: 'Все VPN-туннели' }] },
  { id: 'tunnel-delete', roles: OWNERS, router: 'sandbox-home', steps: [TUNNELS, { tunnelWith: 'Удалить VPN-туннель' }, { click: 'Удалить VPN-туннель' }] },
  { id: 'replace', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { tunnelWith: 'Заменить конфиг' }, { click: 'Заменить конфиг' }] },
  { id: 'routes', roles: ALL, router: 'sandbox-home', steps: ROUTES },
  { id: 'routeadd', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Добавить сайт или адрес' }] },
  { id: 'routepick', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Перенести' }] },
  { id: 'make-default', roles: ALL, router: 'sandbox-home', steps: [...ROUTES, { click: 'Сделать главным' }] },
  { id: 'hrneo-stop', roles: OWNERS, router: 'sandbox-home', steps: [...ROUTES, { click: 'Остановить' }] },
  // Запустить -- только когда HydraRoute Neo остановлен; в песочнице он идёт.
  { id: 'hrneo-start', roles: OWNERS, router: 'sandbox-home', optional: true, steps: [...ROUTES, { click: 'Запустить' }] },
  { id: 'diag-now', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }] },
  { id: 'diag-history', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }, { segment: 'Что было' }] },
  { id: 'manage', roles: ALL, router: 'sandbox-home', steps: MANAGE },
  { id: 'reboot', roles: OWNERS, router: 'sandbox-home', steps: [...MANAGE, { click: 'Перезагрузить роутер' }, { fill: '$router' }] },
  { id: 'agentcfg', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть настройки агента' }] },
  { id: 'agentconn', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть подключение агента' }] },
  { id: 'dnsreset', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть сброс DNS' }] },
  { id: 'packages', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть пакеты по расписанию' }] },
  // Владелец трёх роутеров: ещё один длинный роутер текущим.
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
]
