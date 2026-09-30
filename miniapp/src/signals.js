import { agoText, whenText } from './when.js'

// Тексты v0.47. Словарь экрана: «VPN-туннель» полной формой; ни одна строка
// не обещает «весь трафик через VPN» -- адрес всегда про ЭТОТ VPN-туннель.
export const SIGNAL_TEXTS = {
  needAgent: 'нужен агент v0.47',
  notMeasured: 'не мерили',
  stopped: 'VPN-туннель не работает — не мерили',
  failed: 'замер не удался',
  sameIP: 'Трафик через этот VPN-туннель выходит тем же адресом, что и напрямую: сайты видят ваш домашний адрес.',
  noPingCheck:
    'Без Ping-Check роутер переходит на резерв, только когда кабель отключён совсем. Если провайдер пропал, а кабель на месте, резерв не включится. Настройка: веб-панель роутера → Интернет → Проверка доступности.',
  wanUnsupported: 'эта версия awg-manager не сообщает о подключениях',
  stale: 'на момент последнего отчёта',
  hooksTitle: 'Мгновенная реакция, когда меняется VPN-туннель',
  logsUnsupported: 'Эта версия awg-manager журнал наружу не отдаёт.',
  logsDisabled: 'Журнал выключен в awg-manager.',
  logsEmpty: 'awg-manager ничего не писал за это время.',
  dnsTargetDown: 'VPN-туннель этого списка не работает — сайты из списка сейчас идут напрямую.',
  dnsEmpty: 'В списке нет ни одного сайта.',
  dnsUnverified: 'не проверено: роутер не отдал свои настройки',
}

function ageSec(iso, nowMs) {
  const t = Date.parse(iso ?? '')
  if (Number.isNaN(t)) return null
  return Math.max(0, Math.round((nowMs - t) / 1000))
}

function sitesWord(n) {
  const m10 = n % 10
  const m100 = n % 100
  if (m10 === 1 && m100 !== 11) return 'сайт'
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return 'сайта'
  return 'сайтов'
}

// Строка «Куда выходит трафик» одного VPN-туннеля.
export function exitLine(facts, tunnelID, { running = true, nowMs = Date.now() } = {}) {
  if (!facts || !facts.supported) return { value: SIGNAL_TEXTS.needAgent, tone: 'muted', sub: '', warn: '' }
  const p = facts.exit?.tunnels?.[tunnelID]
  if (!p) return { value: running ? SIGNAL_TEXTS.notMeasured : SIGNAL_TEXTS.stopped, tone: 'muted', sub: '', warn: '' }
  const age = ageSec(p.at, nowMs)
  const sub = age == null ? '' : agoText(age)
  if (p.failed || !p.vpn_ip) return { value: SIGNAL_TEXTS.failed, tone: 'muted', sub, warn: '' }
  if (p.changed === false) return { value: `${p.vpn_ip} — как напрямую`, tone: 'warn', sub, warn: SIGNAL_TEXTS.sameIP }
  const direct = p.direct_ip ? `, напрямую ${p.direct_ip}` : ''
  return { value: `через VPN-туннель ${p.vpn_ip}${direct}`, tone: undefined, sub, warn: '' }
}

// «моргал»: неудачные пробы пингчека awg-manager за сутки. Ноль не показываем:
// у VPN-туннеля без проверки связи ноль значит «не проверяли», а не «всё хорошо».
export function pingFailsLine(facts, tunnelID) {
  if (!facts?.supported) return null
  const n = facts.ping_fails_24h?.[tunnelID] ?? 0
  if (n <= 0) return null
  return { title: 'Неудачных проверок связи за сутки', value: String(n), tone: 'warn' }
}

// «Резервный интернет». Нет резервного подключения -- секции нет.
export function wanView(facts) {
  const wan = facts?.wan
  if (!facts?.supported || !wan) return null
  if (wan.unsupported) return { rows: [], hint: '', note: SIGNAL_TEXTS.wanUnsupported }
  const links = wan.links ?? []
  if (!links.some((l) => l.role === 'backup')) return null
  const rows = links.map((l, i) => ({
    key: `${l.role}-${i}`,
    title: l.role === 'primary' ? 'Основное подключение' : 'Резервное подключение',
    value: `${l.label || 'без названия'} · ${l.up ? 'работает' : 'не работает'}`,
    tone: l.up ? undefined : l.role === 'primary' ? 'danger' : 'muted',
    sub: l.role !== 'backup' ? '' : l.pingcheck === 'unset' ? 'Ping-Check не задан' : l.pingcheck === 'set' ? 'Ping-Check задан' : '',
  }))
  const hint = links.some((l) => l.role === 'backup' && l.pingcheck === 'unset') ? SIGNAL_TEXTS.noPingCheck : ''
  return { rows, hint, note: wan.stale ? SIGNAL_TEXTS.stale : '' }
}

// Строка хука в «Опрос и тревоги».
export function hooksRow(facts, nowMs = Date.now()) {
  const title = SIGNAL_TEXTS.hooksTitle
  if (!facts?.supported) return { title, value: SIGNAL_TEXTS.needAgent, tone: 'muted' }
  const h = facts.hooks
  if (!h) return { title, value: 'агент ещё не сообщил', tone: 'muted' }
  switch (h.state) {
    case 'installed': {
      const age = h.last_wake_at ? ageSec(h.last_wake_at, nowMs) : null
      return { title, value: age == null ? 'включена' : `включена · последний раз ${agoText(age)}`, tone: 'ok' }
    }
    case 'unsupported':
      return { title, value: 'прошивка не умеет — роутер опрашивается по расписанию', tone: 'muted' }
    case 'disabled':
      return { title, value: 'выключена в настройках агента', tone: 'muted' }
    default:
      return { title, value: 'не удалось включить', tone: 'warn' }
  }
}

// «Списки сайтов в самой прошивке». tunnels -- снимок маршрутов ({id, name}).
export function nativeDNSView(facts, tunnels = []) {
  const nd = facts?.native_dns
  if (!facts?.supported || !nd) return null
  if (nd.unverified_reason) return { rows: [], note: SIGNAL_TEXTS.dnsUnverified }
  const lists = nd.lists ?? []
  if (!lists.length) return null
  const names = new Map(tunnels.map((t) => [t.id ?? t.tunnel_id, t.name]))
  const rows = lists.map((l) => {
    const target = l.tunnel_id ? `VPN-туннель «${names.get(l.tunnel_id) || l.tunnel_id}»` : 'интерфейс роутера'
    return {
      key: l.name,
      title: l.name,
      value: `${l.domains} ${sitesWord(l.domains)} → ${target}`,
      sub: l.owner === 'awgm' ? 'завёл awg-manager' : 'заведён в панели роутера',
      tone: l.issue ? 'warn' : undefined,
      warn: l.issue === 'target_down' ? SIGNAL_TEXTS.dnsTargetDown : l.issue === 'empty' ? SIGNAL_TEXTS.dnsEmpty : '',
    }
  })
  return { rows, note: nd.stale ? SIGNAL_TEXTS.stale : '' }
}

// Ответ команды awgm_logs (wire.AwgmLogs).
export function logsView(result) {
  if (!result || result.status !== 'ok') return null
  let data
  try {
    data = JSON.parse(result.output)
  } catch {
    return { state: 'error', rows: [], note: 'Роутер ответил непонятно.' }
  }
  if (data.unsupported) return { state: 'unsupported', rows: [], note: SIGNAL_TEXTS.logsUnsupported }
  if (!data.enabled) return { state: 'disabled', rows: [], note: SIGNAL_TEXTS.logsDisabled }
  const entries = data.entries ?? []
  if (!entries.length) return { state: 'empty', rows: [], note: SIGNAL_TEXTS.logsEmpty }
  const rows = entries.map((e, i) => ({
    key: `${e.ts}-${i}`,
    title: [e.group, e.action].filter(Boolean).join(' · ') || e.level,
    value: e.repeats > 1 ? `${e.message} ×${e.repeats}` : e.message,
    sub: whenText(e.ts),
    tone: e.level === 'error' ? 'danger' : e.level === 'warn' ? 'warn' : undefined,
  }))
  return { state: 'ok', rows, note: data.truncated ? 'Показаны самые свежие записи — остальное не влезло.' : '' }
}
