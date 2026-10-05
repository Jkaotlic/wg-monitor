// Общая фикстура B1 (v0.56): один и тот же роутер глазами двух экранов --
// «Роутер» (ответ /routers/{id}/events: tunnels, traffic, и тревоги строки
// роутера) и «VPN-туннели» (снимок route_status с вердиктом тех же проверок).
// expect.name -- кого оба экрана обязаны назвать несущим (null -- никого),
// expect.alive -- жив ли он.
//
// Имена и адреса обезличены; формы -- как у проекции miniapp_tunnels.go
// (run_state -- слово роутера, status -- вердикт проверки) и wire.RouteSnapshot.

const ONLINE = { status: 'alert', last_seen_age_sec: 10 }

const policy = (active, links) => ({
  name: 'HydraRoute',
  dns: 32,
  hr_neo: 28,
  via_vpn: true,
  active_tunnel_id: active,
  interfaces: links.map(([id, name, role]) => ({ bind: `OpkgTun${id.slice(3)}`, name, role, tunnel_id: id, via_vpn: true })),
})

const snapTunnel = (id, name, status) => ({ id, name, iface: `opkgtun${id.slice(3)}`, type: 'managed', status, has_handshake: true, handshake_age_sec: 30 })

export const CARRIER_SCENARIOS = [
  {
    // workrouter 18.09: первый поднятый (vpn-nl) мёртв, несёт живой vpn-hip.
    title: 'первый running мёртв, несущий жив',
    router: ONLINE,
    reserveOnlyAlert: true,
    incidents: [{ check_name: 'tunnel_awg10' }],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok', matrix_latency_ms: 117 },
      ],
      traffic: { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip', carrier_tunnel_id: 'awg14', carrier_basis: 'policy', carrier_alive: true },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'up'), snapTunnel('awg14', 'vpn-hip', 'up')],
      policies: [policy('awg14', [['awg14', 'vpn-hip', 'active'], ['awg10', 'vpn-nl', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-hip', alive: true },
  },
  {
    // Несущий -- активное звено политики, и он мёртв; живой сосед стоит
    // первым. «Первый running» назвал бы соседа.
    title: 'несущий мёртв, первый running -- живой сосед',
    router: ONLINE,
    incidents: [{ check_name: 'tunnel_awg10' }],
    events: {
      tunnels: [
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok', matrix_latency_ms: 90 },
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
      ],
      traffic: { mode: 'split', egress_tunnel_id: 'awg10', egress_tunnel_name: 'vpn-nl', reserve_tunnel_ids: ['awg14'], carrier_tunnel_id: 'awg10', carrier_basis: 'policy', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg14', 'vpn-hip', 'up'), snapTunnel('awg10', 'vpn-nl', 'up')],
      policies: [policy('awg10', [['awg10', 'vpn-nl', 'active'], ['awg14', 'vpn-hip', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-nl', alive: false },
  },
  {
    // Проверка несущего уже провалена, тревога ещё не набрала порог: «всё
    // работает» было бы неправдой.
    title: 'несущий не отвечает, тревоги ещё нет',
    router: { status: 'ok', last_seen_age_sec: 10 },
    incidents: [],
    events: {
      tunnels: [
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok', matrix_latency_ms: 90 },
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
      ],
      traffic: { mode: 'split', egress_tunnel_id: 'awg10', egress_tunnel_name: 'vpn-nl', reserve_tunnel_ids: ['awg14'], carrier_tunnel_id: 'awg10', carrier_basis: 'policy', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg14', 'vpn-hip', 'up'), snapTunnel('awg10', 'vpn-nl', 'up')],
      policies: [policy('awg10', [['awg10', 'vpn-nl', 'active'], ['awg14', 'vpn-hip', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-nl', alive: false },
  },
  {
    // Песочница sandbox-broken: sing-box выбирает маршрут для каждого адреса,
    // vpn-nl упал, vpn-de жив. До v0.56 «Роутер» называл vpn-de (первый
    // живой), вкладка -- vpn-nl (активное звено политики).
    title: 'sandbox-broken: sing-box',
    singbox: true,
    router: ONLINE,
    incidents: [{ check_name: 'tunnel_awg12' }],
    events: {
      tunnels: [
        { tunnel_id: 'awg12', name: 'vpn-nl', run_state: 'down', status: 'fail' },
        { tunnel_id: 'awg10', name: 'vpn-de', run_state: 'running', status: 'ok', matrix_latency_ms: 226 },
      ],
      traffic: { mode: 'singbox', contested_default: false, carrier_basis: 'none', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg12', 'vpn-nl', 'up'), snapTunnel('awg10', 'vpn-de', 'down'), snapTunnel('awg14', 'vpn-spare', 'up')],
      policies: [policy('awg12', [['awg12', 'vpn-nl', 'active'], ['awg10', 'vpn-de', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg12', status: 'fail' }, { tunnel_id: 'awg10', status: 'ok' }] },
    expect: { name: null },
  },
  {
    // Сервер несущего не знает (старый агент без сводки политик), а снимок
    // маршрутов роутера называет активное звено -- это слово роутера, а не
    // «первый running» (первым стоит мёртвый vpn-nl). Оба экрана называют его.
    title: 'сервер не знает, снимок роутера называет',
    router: ONLINE,
    incidents: [{ check_name: 'tunnel_awg10' }],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok' },
      ],
      traffic: { mode: 'split', contested_default: true, carrier_basis: 'none', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'up'), snapTunnel('awg14', 'vpn-hip', 'up')],
      policies: [policy('awg14', [['awg14', 'vpn-hip', 'active'], ['awg10', 'vpn-nl', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-hip', alive: true },
  },
  {
    // Старый агент, в политиках снимка нет active_tunnel_id: несущего не
    // знает никто. Шапка -- «всё работает» (правила уводят), вкладка --
    // «не знаем», а не красное «ни один VPN-туннель не несёт трафик».
    title: 'старый агент, политики без активного звена',
    router: { status: 'ok', last_seen_age_sec: 10 },
    incidents: [],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'ok' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok' },
      ],
      traffic: { mode: 'split', contested_default: true, carrier_basis: 'none', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'up'), snapTunnel('awg14', 'vpn-hip', 'up')],
      policies: [{ ...policy('', [['awg14', 'vpn-hip', 'active'], ['awg10', 'vpn-nl', 'fallback']]), active_tunnel_id: undefined }],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'ok' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: null, headlineTone: 'sig' },
  },
  {
    // То же, но один VPN-туннель упал: кто из двух нёс -- неизвестно, и
    // «работает на запасном» было бы догадкой.
    title: 'несущий неизвестен, один VPN-туннель упал',
    router: ONLINE,
    incidents: [{ check_name: 'tunnel_awg10' }],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'fail' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok' },
      ],
      traffic: { mode: 'split', contested_default: true, carrier_basis: 'none', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'up'), snapTunnel('awg14', 'vpn-hip', 'up')],
      policies: [{ ...policy('', [['awg14', 'vpn-hip', 'active'], ['awg10', 'vpn-nl', 'fallback']]), active_tunnel_id: undefined }],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: null },
  },
  {
    // Главный выход напрямую, а несущий политики лежит: не «трафик идёт
    // напрямую», а «VPN-туннель обхода не отвечает» -- на обоих экранах.
    title: 'напрямую, несущий политики мёртв',
    router: { status: 'ok', last_seen_age_sec: 10 },
    incidents: [],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'stopped', status: 'fail' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'stopped', status: 'ok' },
      ],
      traffic: { mode: 'direct', carrier_tunnel_id: 'awg10', carrier_basis: 'policy', carrier_alive: false },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'down'), snapTunnel('awg14', 'vpn-hip', 'stopped')],
      policies: [policy('awg10', [['awg10', 'vpn-nl', 'active'], ['awg14', 'vpn-hip', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'fail' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-nl', alive: false, headlineTag: 'VPN-туннель обхода не отвечает' },
  },
  {
    // Главный выход роутер не назвал (routeTag не прочитан), а несущий
    // политики известен и жив: шапка называет его, а не «роутер не сообщил».
    title: 'главный выход неизвестен, несущий политики жив',
    router: { status: 'ok', last_seen_age_sec: 10 },
    incidents: [],
    events: {
      tunnels: [
        { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'ok' },
        { tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'ok', matrix_latency_ms: 70 },
      ],
      traffic: { mode: 'unknown', carrier_tunnel_id: 'awg14', carrier_basis: 'policy', carrier_alive: true },
    },
    snapshot: {
      tunnels: [snapTunnel('awg10', 'vpn-nl', 'up'), snapTunnel('awg14', 'vpn-hip', 'up')],
      policies: [policy('awg14', [['awg14', 'vpn-hip', 'active'], ['awg10', 'vpn-nl', 'fallback']])],
    },
    checks: { tunnels: [{ tunnel_id: 'awg10', status: 'ok' }, { tunnel_id: 'awg14', status: 'ok' }] },
    expect: { name: 'vpn-hip', alive: true },
  },
]
