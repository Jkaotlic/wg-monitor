// Раздельный DNS: чистые функции раздела на экране «Проверки». Проверка
// dns_split агента отвечает кодами (internal/agent/checks/dns_split.go), слова
// для владельца подбираются здесь.
//
// Источник вердикта -- НАСТРОЙКИ роутера, а не замер: Яндекс и заграничные
// DNS-серверы отдают одинаковые адреса на крупные русские сайты, и сравнение
// ответов ничего не различает (прогон 14.09.2026). Поэтому слов уверенности
// здесь нет, а оговорка стоит рядом с ответом.

import { agentAtLeast, agentVersionKnown } from './agentConfig.js'

// Имена зон так, как их пишет человек, а не роутер (punycode).
const ZONE_NAMES = {
  'xn--p1ai': 'рф',
  'xn--80adxhks': 'москва',
  'xn--d1acj3b': 'дети',
  'xn--p1acf': 'рус',
}

// Порядок групп: поломка схемы первой -- человек видит её раньше нормы.
const GROUPS = [
  { key: 'other', tone: 'warn', lead: 'Отданы другому DNS-серверу, не Яндексу' },
  {
    key: 'mixed',
    tone: 'warn',
    lead: 'Поделены между Яндексом и другим DNS-сервером — часть запросов уйдёт не к Яндексу',
  },
  { key: 'yandex_doh', tone: 'warn', lead: 'Яндекс, но не тем способом, что в эталоне' },
  { key: 'none', tone: 'warn', lead: 'Без отдельного правила — уходят на общие DNS-серверы' },
  { key: 'unknown', tone: 'muted', lead: 'Неизвестно — роутер не отдал свои настройки' },
  { key: 'yandex_dot', tone: 'ok', lead: 'Яндекс, защищённое соединение' },
]

function zoneName(zone) {
  return `.${ZONE_NAMES[zone] ?? zone}`
}

function routeLine(d) {
  switch (d.route) {
    case 'direct':
      return { text: 'Запросы к Яндексу, по правилам роутера, идут напрямую, мимо VPN-туннеля.', tone: 'ok' }
    case 'tunnel':
      return {
        text: `Запросы к Яндексу идут через VPN-туннель «${d.route_tunnel || 'без имени'}» — Яндекс видит заграничный адрес и может отдавать адреса сайтов не для России.`,
        tone: 'warn',
      }
    case 'mixed':
      return { text: 'Правила роутера ведут запросы к Яндексу в разные места.', tone: 'warn' }
    default:
      return { text: 'Не удалось узнать, как идут запросы к Яндексу.', tone: 'muted' }
  }
}

// Агент, с которого проверка dns_split приходит в отчёте (cmd/agent/main.go
// подключил её в v0.31.0). Старше -- «появится после обновления»; новее --
// проверка просто ещё не пришла (v0.56, B2): обновлять агента незачем.
export const DNS_SPLIT_MIN_VERSION = 'v0.31.0'

export const DNS_SPLIT_TEXTS = {
  tooOld: 'Эта проверка появится после обновления агента на роутере.',
  notYet: 'Проверка ещё не пришла: она приходит с каждым отчётом роутера.',
  unknownAgent: 'Проверка ещё не пришла. Версию агента роутер не сообщил, поэтому сказать, дело в версии или во времени, нельзя.',
}

// Что сказать, пока проверки нет, -- по версии агента, а не «на всякий случай».
function missingNote(agentVersion) {
  if (!agentVersionKnown(agentVersion)) return DNS_SPLIT_TEXTS.unknownAgent
  return agentAtLeast(agentVersion, DNS_SPLIT_MIN_VERSION) ? DNS_SPLIT_TEXTS.notYet : DNS_SPLIT_TEXTS.tooOld
}

// dnsSplitView(checks, { silent }) -> { missing, note, rows, route, resolves, foot }.
// rows -- одна строка на группу зон с одинаковым вердиктом:
// { key, tone, lead, zones, text }.
export function dnsSplitView(checks, { silent = false, agentVersion = '' } = {}) {
  const c = (checks ?? []).find((x) => x.check_name === 'dns_split')
  if (!c) {
    // Старый агент такой проверки не присылает: это не «нет данных», а
    // «появится после обновления». Новый -- проверка просто ещё не пришла.
    return {
      missing: true,
      note: missingNote(agentVersion),
      rows: [],
      route: null,
      resolves: null,
      foot: [],
    }
  }
  const d = c.details ?? {}
  const zones = d.zones && typeof d.zones === 'object' ? d.zones : {}
  const byVerdict = new Map()
  for (const [zone, verdict] of Object.entries(zones)) {
    const key = GROUPS.some((g) => g.key === verdict) ? verdict : 'unknown'
    if (!byVerdict.has(key)) byVerdict.set(key, [])
    byVerdict.get(key).push(zoneName(zone))
  }
  // lead и zones -- для строки данных (описание слева, зоны справа), text --
  // та же мысль одной фразой.
  const rows = GROUPS.filter((g) => byVerdict.has(g.key)).map((g) => {
    const list = byVerdict.get(g.key).join(', ')
    return { key: g.key, tone: g.tone, lead: g.lead, zones: list, text: `${g.lead}: ${list}` }
  })

  const resolves =
    d.resolves === 'fail'
      ? { text: 'Роутер не ответил на запрос имени сайта — сайты по имени могут не открываться.', tone: 'warn' }
      : null

  const foot = [
    silent
      ? 'Пока роутер молчит, это данные на момент последнего отчёта, а не на сейчас.'
      : 'Это вывод по настройкам роутера, а не замер трафика.',
    'Какой адрес видит сам банк, решает маршрут до банка — проверьте его в «Куда пойдёт сайт» на экране маршрутов.',
  ]

  return { missing: false, note: '', rows, route: routeLine(d), resolves, foot }
}
