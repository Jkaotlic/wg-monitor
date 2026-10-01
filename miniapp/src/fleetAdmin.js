// Админский экран парка: то, ради чего дашборд открывают каждый день.
//
// Здесь только чистые функции -- экран собирает из них строки. Правило то же,
// что на остальных экранах: строка отвечает не «сколько», а «что не так», и
// состояние говорится словом, а не цветом.
//
// Ничего из этого клиент не пересчитывает сам: «пора обновить» и тексты про
// срок ссылки приходят с сервера. Вторая копия сравнения версий разошлась бы
// с первой, а второй текст про 12 часов -- с тем, что сказал бот.
import { humanAge, incidentWhatPlain, pluralRu } from './labels.js'
import { agoText } from './when.js'
import { agentUpdateState, isAway } from './agentUpdate.js'
import { isStale, reachStatus } from './staleness.js'
import { fleetRow, redAlert } from './fleet.js'

export const EMPTY_PARK = 'В парке нет ни одного роутера.'

// Строка о бэкенде: своя версия и «доступна X», если вышла новее. Судит о
// новизне сервер -- у него же лежит сравнение версий.
export function backendRow(fleet) {
  const backend = fleet?.backend ?? {}
  return {
    value: backend.version ?? '',
    sub: backend.update_available && backend.latest_version ? `доступна ${backend.latest_version}` : '',
  }
}

// Порядок карточек Парка (v0.50, спека п. 2.4): тревога → молчит → агент
// отстаёт → остальные. Молчащая тревога -- среди молчащих (reachStatus):
// пока нет связи, чинить её нечем.
export function parkRank(router) {
  if (router?.last_seen_age_sec == null) return 1
  if (redAlert(router)) return 0
  const s = reachStatus(router)
  if (s === 'offline' || s === 'sleeping') return 1
  if (router?.agent_behind) return 2
  return 3
}

// listRouters -- список /routers оболочки: пилюля карточки берётся из него,
// чтобы Парк и «Мои роутеры» говорили одно слово (резерв, молчание).
export function fleetRouterRows(fleet, listRouters = []) {
  const list = fleet?.routers ?? []
  const backendVersion = fleet?.backend?.version ?? ''
  const byID = new Map((listRouters ?? []).map((x) => [x.id, x]))
  // Признак «тревога только по запасному звену» -- у строки /routers: в порядке
  // карточек его тоже учитываем, иначе Парк и полоса разойдутся.
  const withReserve = (r) => ({ ...r, reserve_only_alert: byID.get(r?.id)?.reserve_only_alert ?? r?.reserve_only_alert })
  return [...list]
    .sort((a, b) => parkRank(withReserve(a)) - parkRank(withReserve(b)) || (a?.nickname ?? '').localeCompare(b?.nickname ?? '', 'ru'))
    .map((router) => ({
      id: router?.id,
      name: router?.nickname ?? '',
      pill: fleetRow(byID.get(router?.id) ?? router).pill,
      sub: routerSub(router),
      versions: versionsLine(router, backendVersion),
      hint: router?.update_hint ?? '',
      update: agentUpdateState(router),
      warning: router?.agent_update_warning ?? '',
      notify: notifySwitch(router),
      router,
    }))
}

// Одна строка под именем: где роутер (или что с ним) и что с агентом --
// обновление, если оно в пути, иначе версия.
export function parkCardLine(row, rv) {
  const agent = row?.router?.agent_version ? `агент ${row.router.agent_version}` : ''
  // Оживление в пути важнее версии: оно объясняет, почему роутер молчит.
  const tail = rv?.text ? `оживление: ${rv.text}` : row?.update?.text || agent
  return [row?.sub, tail].filter(Boolean).join(' · ')
}

// Тон строки карточки: оживление, иначе обновление агента. Красное и
// жёлтое остаются красным и жёлтым, как на прежней карточке; приглушённое и
// «всё хорошо» цвета не получают.
export function parkCardTone(row, rv) {
  const tone = rv?.text ? rv.tone : row?.update?.text ? row.update.tone : ''
  return ['warn', 'danger', 'sig', 'ok'].includes(tone) ? tone : ''
}

export function warningFoldTitle(n) {
  return `Что может помешать обновлению · ${n}`
}

function routerSub(router) {
  const incidents = router?.incidents ?? []
  // Имена тревог -- по-русски и без машинных идентификаторов: в списке парка
  // нет ни снимка маршрутов, ни имён VPN-туннелей, чтобы понять «awg12».
  const age = router?.last_seen_age_sec
  // Молчащая тревога: сначала -- что роутер молчит (тревога вчерашняя, пока
  // нет связи), потом -- какая тревога висит (staleness.js, v0.46).
  const away = age != null && (isStale(router) || isAway(router))
  if (incidents.length > 0) {
    const what = incidents.map(incidentWhatPlain).join('; ')
    return away ? `не на связи ${humanAge(age)} · ${what}` : what
  }
  if (age == null) return 'отчётов от него ещё не было'
  if (away) return `не на связи ${humanAge(age)}`
  return `отчёт ${agoText(age)}`
}

// «агент X · бэкенд Y» стоят рядом намеренно: отставание видно глазом, без
// второго экрана и без сравнения версий в клиенте.
function versionsLine(router, backendVersion) {
  const parts = []
  if (router?.agent_version) {
    parts.push(`агент ${router.agent_version}`)
    if (backendVersion) parts.push(`бэкенд ${backendVersion}`)
  }
  if (router?.awgmgr_version) parts.push(`панель ${router.awgmgr_version}`)
  if (router?.firmware_current) parts.push(`прошивка ${router.firmware_current}`)
  return parts.join(' · ')
}

// Дыры уведомлений говорят ПОСЛЕДСТВИЕ, а не код ошибки Telegram: «403» не
// говорит человеку ничего, «не получит тревогу» говорит всё.
export function notifyGapLines(fleet) {
  const notify = fleet?.notify ?? {}
  const lines = []
  for (const target of notify.unreachable ?? []) {
    lines.push(
      `Бот не может написать человеку ${target?.telegram_user_id}: этот человек не получит тревогу, пока сам не напишет боту.`,
    )
  }
  for (const name of notify.routers_without_recipients ?? []) {
    lines.push(`«${name}»: уведомлять некому — ни владельца, ни операторов.`)
  }
  return lines
}

// Что сказать человеку про выданную ссылку. Текст берётся из ответа сервера:
// срок и лимит живут в одном месте, и бот с приложением не могут разойтись в
// том, сколько ссылка живёт.
//
// Саму ссылку на экране не повторяем: она уже ушла в браузер, а оставленная
// на экране она превращается в то, что пересылают.
export function webLinkLines(grant) {
  return [grant?.notice, grant?.limit_notice].filter(Boolean)
}

// «Уведомлять меня» -- личный выключатель админа по роутеру.
//
// Решение оператора: «отключить уведомления в личку от определённого роутера,
// но и одновременно при желании зайти глянуть, что не так». Выключатель
// касается ТОЛЬКО сообщений: роутер остаётся в парке, кнопки и экраны
// работают. Поэтому ни одна функция здесь не фильтрует список.
export function notifySwitch(router) {
  const muted = Boolean(router?.notify_muted)
  return {
    on: !muted,
    note: muted ? 'Бот не пишет вам про этот роутер. Его экраны открываются как обычно.' : '',
  }
}

export function notifyMuteSheetText(router) {
  const name = router?.nickname ?? ''
  return {
    title: `Не уведомлять вас про «${name}»?`,
    body:
      `Бот перестанет писать вам в личку про «${name}»: тревоги, «починилось», «не на связи». ` +
      'Роутер останется в парке, его экраны открываются как обычно. Вернуть — этим же переключателем.',
  }
}

export function withNotifyMuted(fleet, routerID, muted) {
  if (!fleet) return fleet
  return {
    ...fleet,
    routers: (fleet.routers ?? []).map((r) => (r.id === routerID ? { ...r, notify_muted: muted } : r)),
  }
}

// --- Оговорки обновления агента (v0.48) --------------------------------------
//
// agent_update_warning -- фразы из короткого набора сервера
// (agent_update_verdict.go), склеенные «; »: «старая проверка места…»,
// «проверяет адрес загрузки…», «агент слишком старый…». Одна и та же фраза
// под каждым из семи роутеров -- шум, поэтому Парк говорит её один раз над
// списком и перечисляет, кого она касается; на карточке -- метка.
//
// «Агент слишком старый» -- не оговорка к обновлению, а причина, почему
// кнопки «Обновить агент» у роутера нет вовсе (B6): она остаётся на его
// карточке, иначе пропажа кнопки была бы необъяснима.
const CARD_ONLY = 'агент слишком старый'

function warningClauses(warning) {
  return String(warning ?? '')
    .split(';')
    .map((part) => part.trim().replace(/[.\s]+$/, ''))
    .filter(Boolean)
}

export function fleetWarningNotes(rows = []) {
  const byText = new Map()
  for (const row of rows) {
    for (const text of warningClauses(row?.warning)) {
      if (text.startsWith(CARD_ONLY)) continue
      if (!byText.has(text)) byText.set(text, [])
      byText.get(text).push(row.name)
    }
  }
  return [...byText].map(([text, names]) => ({ text, names }))
}

export function cardWarning(row) {
  const clauses = warningClauses(row?.warning)
  return {
    text: clauses.filter((t) => t.startsWith(CARD_ONLY)).join('; '),
    tagged: clauses.some((t) => !t.startsWith(CARD_ONLY)),
  }
}
