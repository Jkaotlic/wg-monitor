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
import { agentUpdateState } from './agentUpdate.js'

export const EMPTY_PARK = 'В парке нет ни одного роутера.'

// Состояние словом. Тот же словарь, что у бота: «работает» / «есть тревога» /
// «молчит» / «спит».
const STATE_WORD = {
  alert: 'есть тревога',
  offline: 'молчит',
  sleeping: 'спит',
  online: 'работает',
}

// Порядок -- по срочности: сломанное сверху.
const URGENCY = { alert: 0, offline: 1, sleeping: 2, online: 3 }

export function fleetHeadline(fleet) {
  const totals = fleet?.totals ?? {}
  const routers = totals.routers ?? 0
  if (!routers) return EMPTY_PARK
  const parts = [`${totals.online ?? 0} на связи`]
  if (totals.sleeping) parts.push(`${totals.sleeping} спит`)
  if (totals.offline) parts.push(`${totals.offline} молчит`)
  if (totals.alerts) {
    parts.push(`${totals.alerts} ${pluralRu(totals.alerts, 'с тревогой', 'с тревогами', 'с тревогами')}`)
  }
  const word = pluralRu(routers, 'роутер', 'роутера', 'роутеров')
  return `${routers} ${word}: ${parts.join(', ')}.`
}

// Строка о бэкенде: своя версия и «доступна X», если вышла новее. Судит о
// новизне сервер -- у него же лежит сравнение версий.
export function backendRow(fleet) {
  const backend = fleet?.backend ?? {}
  return {
    value: backend.version ?? '',
    sub: backend.update_available && backend.latest_version ? `доступна ${backend.latest_version}` : '',
  }
}

export function fleetRouterRows(fleet) {
  const list = fleet?.routers ?? []
  const backendVersion = fleet?.backend?.version ?? ''
  return [...list]
    .sort((a, b) => {
      const ua = URGENCY[a?.status] ?? 99
      const ub = URGENCY[b?.status] ?? 99
      if (ua !== ub) return ua - ub
      return (a?.nickname ?? '').localeCompare(b?.nickname ?? '', 'ru')
    })
    .map((router) => ({
      id: router?.id,
      name: router?.nickname ?? '',
      state: STATE_WORD[router?.status] ?? router?.status ?? '',
      sub: routerSub(router),
      versions: versionsLine(router, backendVersion),
      // Готовая фраза сервера. Пустая строка -- это «обновлять нечего», а не
      // «мы не знаем»: про незнание сервер говорит отдельно.
      hint: router?.update_hint ?? '',
      // Обновление агента -- своим полем: у строки про него есть кнопки, и
      // склеенное в строку версий «ставится vX» кнопкам не за что держаться.
      update: agentUpdateState(router),
      warning: router?.agent_update_warning ?? '',
      notify: notifySwitch(router),
      router,
    }))
}

function routerSub(router) {
  const incidents = router?.incidents ?? []
  // Имена тревог -- по-русски и без машинных идентификаторов: в списке парка
  // нет ни снимка маршрутов, ни имён VPN-туннелей, чтобы понять «awg12».
  if (incidents.length > 0) return incidents.map(incidentWhatPlain).join('; ')
  const age = router?.last_seen_age_sec
  if (age == null) return 'отчётов от него ещё не было'
  if (router?.status === 'sleeping' || router?.status === 'offline') return `не на связи ${humanAge(age)}`
  return `отчёт ${humanAge(age)} назад`
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

// Сторож парка. Пустая строка означает «сторожа в этой сборке нет» -- и это
// честнее, чем нарисовать мёртвого сторожа там, где его просто не подключали.
export function watchdogLine(fleet) {
  const watchdog = fleet?.watchdog
  if (!watchdog) return ''
  const parts = []
  parts.push(watchdog.last_scan_at ? `последний обход ${shortTime(watchdog.last_scan_at)}` : 'обхода ещё не было')
  const errors = watchdog.offline_errors ?? 0
  parts.push(
    errors
      ? `${errors} ${pluralRu(errors, 'отправка не ушла', 'отправки не ушли', 'отправок не ушло')}`
      : 'отправки уходят',
  )
  return parts.join(', ')
}

function shortTime(iso) {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  return at.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
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
