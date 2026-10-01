// Обход приёмки раскладки (спека §9): каждая вкладка и каждый слой × роли.
// Подписи -- словарь v0.52; optional -- кнопки может не быть по данным
// песочницы (тогда экран пропускается с пометкой, а не падает).
export const ROLES = ['admin', 'owner1', 'owner3', 'operator', 'issuer']
export const WIDTHS = [360, 390, 1024, 1440]

const ALL = ROLES
const TUNNELS = { tab: 'VPN-туннели' }
const NEW = [TUNNELS, { click: 'Новый VPN-туннель' }]
const PARK = { tab: 'Парк' }
const MANAGE = [{ tab: 'Настройки' }, { expandAll: true }]

// Порядок: экраны беды -- первыми, пока тревога песочницы свежая (стареет за 5 мин).
export const SCREENS = [
  { id: 'repair', roles: ['owner3', 'admin'], router: 'sandbox-broken', optional: true, steps: [{ tab: 'Роутер' }, { click: 'Починить' }] },
  { id: 'router', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Роутер' }] },
  { id: 'tunnels', roles: ALL, router: 'sandbox-home', steps: [TUNNELS] },
  { id: 'new-tunnel-sheet', roles: ALL, router: 'sandbox-home', steps: NEW },
  { id: 'cabinet', roles: ALL, router: 'sandbox-home', steps: [...NEW, { sheetChoice: 'Amnezia' }] },
  { id: 'cabinet-issue', roles: ALL, router: 'sandbox-home', optional: true, steps: [...NEW, { sheetChoice: 'Amnezia' }, { first: '.cabinet-option-main:not([disabled])' }] },
  { id: 'cabinet-awg3', roles: ['admin', 'issuer'], router: 'sandbox-work', steps: [...NEW, { sheetChoice: 'Панель VPN-сервера' }] },
  { id: 'confimport', roles: ['admin', 'owner1', 'owner3'], router: 'sandbox-home', steps: [...NEW, { sheetChoice: 'Загрузить .conf' }] },
  { id: 'tunnel', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { rowIn: 'Все VPN-туннели' }] },
  { id: 'replace', roles: ALL, router: 'sandbox-home', optional: true, steps: [TUNNELS, { tunnelWith: 'Заменить конфиг' }, { click: 'Заменить конфиг' }] },
  { id: 'routes', roles: ALL, router: 'sandbox-home', steps: [TUNNELS, { click: 'Маршруты: куда идёт трафик' }] },
  { id: 'routeadd', roles: ALL, router: 'sandbox-home', optional: true, steps: [TUNNELS, { click: 'Маршруты: куда идёт трафик' }, { click: 'Добавить сайт или адрес' }] },
  { id: 'routepick', roles: ALL, router: 'sandbox-home', optional: true, steps: [TUNNELS, { click: 'Маршруты: куда идёт трафик' }, { click: 'Перенести' }] },
  { id: 'diag-now', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }] },
  { id: 'diag-history', roles: ALL, router: 'sandbox-home', steps: [{ tab: 'Проверки' }, { segment: 'Что было' }] },
  { id: 'manage', roles: ALL, router: 'sandbox-home', steps: MANAGE },
  { id: 'agentcfg', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть настройки агента' }] },
  { id: 'agentconn', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть подключение агента' }] },
  { id: 'dnsreset', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть сброс DNS' }] },
  { id: 'packages', roles: ['admin'], router: 'sandbox-home', steps: [...MANAGE, { click: 'Открыть пакеты по расписанию' }] },
  { id: 'fleet-list', roles: ['admin'], steps: [PARK, { headerPick: true }] },
  { id: 'park', roles: ['admin'], steps: [PARK] },
  { id: 'selfhosted-vps', roles: ['admin'], steps: [PARK, { click: 'Свои VPS' }] },
  { id: 'selfhosted-inst', roles: ['admin'], steps: [PARK, { click: 'Свои VPS' }, { first: '.overlay .list-row-btn' }] },
  { id: 'awg3-list', roles: ['admin'], steps: [PARK, { click: 'Панели VPN-серверов' }] },
  { id: 'awg3panel', roles: ['admin'], steps: [PARK, { click: 'Панели VPN-серверов' }, { click: 'Main (Амстердам)' }] },
  { id: 'awg3panel-down', roles: ['admin'], steps: [PARK, { click: 'Панели VPN-серверов' }, { click: 'Старый пароль' }] },
  { id: 'awg3form', roles: ['admin'], steps: [PARK, { click: 'Панели VPN-серверов' }, { click: 'Main (Амстердам)' }, { click: 'Настройки панели' }] },
  { id: 'provision', roles: ['admin'], steps: [PARK, { click: 'Добавить роутер' }] },
]
