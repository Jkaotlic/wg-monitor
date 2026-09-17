// Обслуживание роутера: чистые функции экрана настроек.
//
// Кнопки переехали из панели обслуживания бота (цикл 1 «бот без слеш-команд»).
// Правила, которые здесь важнее вёрстки:
//
//   1. Видят их админ, владелец и операторы -- тот же круг, что у сервера.
//   2. Обновления awg-manager и HydraRoute Neo -- только агенту от v0.32.0:
//      старый ответил бы «unknown action». Сервер отказывает сам, экран не
//      рисует кнопку -- две независимые преграды.
//   3. Прошивка и перезагрузка -- с набором имени роутера; сервер его сверяет.
//   4. Владельцу не показываются внутренние имена: действие называется
//      commandLabel, ошибки агента пересказываются словами.

import { confirmSheet } from './sheet.js'
import { agentAtLeast } from './agentConfig.js'
import { AGENT_OLDER_THAN_APP } from './labels.js'

export const MAINTENANCE_MIN_VERSION = 'v0.32.0'

export const MAINT_TEXTS = {
  tooOld: 'Обновление awg-manager и HydraRoute Neo из приложения появится после обновления агента на роутере.',
  hrneoMissing: 'HydraRoute Neo на роутере не установлен.',
  rebootBanner: 'Сменился модуль ядра AmneziaWG — VPN-туннели поднимутся после перезагрузки роутера.',
  rebootForbidden: 'Перезагрузка с роутера запрещена в настройках агента.',
  firmwareForbidden: 'Установка прошивки запрещена в настройках агента.',
  busy: 'На роутере уже идёт обновление пакетов — дождитесь, пока оно закончится.',
  noSpace: 'Не хватит места для пакетов Entware — освободите место на накопителе роутера и повторите.',
}

const ROLES = new Set(['admin', 'owner', 'operator'])

export function mayMaintain(settings) {
  return ROLES.has(settings?.role)
}

export function updatesAgentReady(settings) {
  return agentAtLeast(settings?.agent_version, MAINTENANCE_MIN_VERSION)
}

export function updateRow(versions, component) {
  return (versions?.rows ?? []).find((r) => r.component === component) ?? null
}

// Кнопку HydraRoute Neo прячет только явное «не установлен». Молчание опроса --
// не ответ: у владельца, у которого HydraRoute стоит, кнопка обязана остаться.
export function hrneoButtonVisible(versions) {
  return versions?.installed?.hrneo_installed !== false
}

export function awgmUpdateSheet({ routerID, row, asleep = false, onResult }) {
  return confirmSheet({
    routerID,
    title: 'Обновить awg-manager?',
    body: `Версия ${row.installed} → ${row.available}. VPN-туннели на несколько секунд переподключатся.`,
    action: 'awgm_update',
    buttonLabel: 'Обновить',
    commandLabel: 'обновление awg-manager',
    asleep,
    onResult,
  })
}

// Внешнего источника «последней версии» у HydraRoute Neo нет: если новости
// нет, «станет» говорит словами, откуда возьмётся версия.
export function hrneoUpdateSheet({ routerID, installed = '', available = '', asleep = false, onResult }) {
  const from = installed || 'стоящая сейчас'
  const to = available || 'последняя из пакетов Entware'
  return confirmSheet({
    routerID,
    title: 'Проверить и обновить HydraRoute Neo?',
    body: `Версия ${from} → ${to}. VPN-туннели на несколько секунд переподключатся.`,
    action: 'hrneo_update',
    buttonLabel: 'Обновить',
    commandLabel: 'обновление HydraRoute Neo',
    asleep,
    onResult,
  })
}

export function firmwareSheet({ routerID, routerName, current, available, asleep = false, onResult, onDone }) {
  return confirmSheet({
    routerID,
    title: `Поставить прошивку ${available}?`,
    body: `Роутер «${routerName}» скачает ${available} вместо ${current} и перезагрузится. VPN-туннели упадут на несколько минут. Вернуть прежнюю версию из приложения нельзя.`,
    action: 'firmware_install',
    buttonLabel: 'Поставить и перезагрузить',
    commandLabel: 'установка прошивки',
    danger: true,
    asleep,
    confirmPhrase: routerName || '',
    onResult,
    onDone,
  })
}

export function rebootSheet({ routerID, routerName, asleep = false, onResult }) {
  return confirmSheet({
    routerID,
    title: `Перезагрузить роутер «${routerName}»?`,
    body: 'Роутер перезагрузится: интернет и VPN-туннели пропадут на две–три минуты, потом поднимутся сами.',
    action: 'service_restart',
    args: { name: 'router' },
    buttonLabel: 'Перезагрузить',
    commandLabel: 'перезагрузка роутера',
    danger: true,
    asleep,
    confirmPhrase: routerName || '',
    onResult,
  })
}

const RESTART = {
  hrneo: {
    title: 'Перезапустить HydraRoute Neo?',
    body: 'HydraRoute Neo перезапустится за несколько секунд. На это время правила по именам сайтов перестанут работать, потом всё вернётся само.',
    label: 'перезапуск HydraRoute Neo',
  },
  awgmgr: {
    title: 'Перезапустить awg-manager?',
    body: 'Панель роутера на несколько секунд перестанет отвечать. VPN-туннели при этом не разрываются.',
    label: 'перезапуск awg-manager',
  },
}

export function restartSheet({ routerID, name, asleep = false, onResult }) {
  const t = RESTART[name]
  return confirmSheet({
    routerID,
    title: t.title,
    body: t.body,
    action: 'service_restart',
    args: { name },
    buttonLabel: 'Перезапустить',
    commandLabel: t.label,
    asleep,
    onResult,
  })
}

export function opkgUpgradeSheet({ routerID, asleep = false, onResult }) {
  return confirmSheet({
    routerID,
    title: 'Обновить пакеты Entware?',
    body: 'Роутер скачает свежие списки пакетов, проверит свободное место и обновит всё устаревшее. Обновление может перезапустить службы роутера: HydraRoute Neo и awg-manager на несколько секунд перестанут отвечать.',
    action: 'opkg_upgrade',
    buttonLabel: 'Обновить пакеты',
    commandLabel: 'обновление пакетов Entware',
    danger: true,
    asleep,
    onResult,
  })
}

export function feedDisableSheet({ routerID, feed, asleep = false, onResult }) {
  return confirmSheet({
    routerID,
    title: `Отключить источник пакетов ${feed.host}?`,
    body: `Источник не отвечает, и из-за него обновление пакетов идёт не целиком. Роутер закомментирует его в настройках и проверит обновления заново. Адрес: ${feed.url}`,
    action: 'opkg_feed_disable',
    args: { url: feed.url },
    buttonLabel: 'Отключить фид',
    commandLabel: 'отключение источника пакетов',
    asleep,
    onResult,
  })
}

function parseJSONObject(output) {
  if (typeof output !== 'string' || output.trim() === '') return null
  try {
    const v = JSON.parse(output)
    return v && typeof v === 'object' ? v : null
  } catch {
    return null
  }
}

// parseAwgmUpdate -- wire.AwgmUpdateResult из успешного ответа, иначе null.
export function parseAwgmUpdate(result) {
  if (result?.status !== 'ok') return null
  const v = parseJSONObject(result.output)
  if (!v || typeof v.updated !== 'boolean') return null
  return { updated: v.updated, from: v.from ?? '', to: v.to ?? '', rebootNeeded: v.reboot_needed === true }
}

export function awgmUpdateText(result) {
  if (result?.status === 'timeout') return 'Роутер не ответил вовремя — проверьте версию awg-manager через минуту.'
  const parsed = parseAwgmUpdate(result)
  if (parsed) {
    if (!parsed.updated) return 'Уже стоит последняя версия awg-manager.'
    const tail = parsed.rebootNeeded ? ' Сменился модуль ядра — нужна перезагрузка роутера.' : ''
    return `awg-manager обновлён: ${parsed.from} → ${parsed.to}.${tail}`
  }
  const out = String(result?.output ?? '')
  if (out.includes('не вернулся с новой версией')) return 'awg-manager не вернулся с новой версией за 5 минут.'
  if (out.includes('отказался обновляться')) return 'awg-manager отказался обновляться.'
  // errAwgmStillChecking (action/awgm_update.go) -- автоустановщик awg-manager
  // ещё считает результат сам; это не отказ роутера, и не должно звучать как
  // общая ошибка.
  if (out.includes('ещё проверяет')) return 'awg-manager ещё проверяет обновления — повторите через минуту.'
  return 'Не удалось обновить awg-manager — роутер ответил ошибкой.'
}

export function hrneoUpdateText(result) {
  if (result?.status === 'locked') return MAINT_TEXTS.busy
  if (result?.status === 'ok') {
    const v = parseJSONObject(result.output)
    if (v && typeof v.updated === 'boolean') {
      return v.updated ? `HydraRoute Neo обновлён: ${v.from} → ${v.to}.` : 'Уже стоит последняя версия HydraRoute Neo.'
    }
  }
  const out = String(result?.output ?? '')
  if (out.includes('не установлен')) return MAINT_TEXTS.hrneoMissing
  if (out.includes('Не хватит места')) return MAINT_TEXTS.noSpace
  if (out.includes('не запустился')) return 'HydraRoute Neo обновлён, но не запустился после перезапуска — перезапустите его ещё раз.'
  return 'Не удалось обновить HydraRoute Neo — роутер ответил ошибкой.'
}

// Тот же вид адреса, что у бота (notifierNormalizeFeedURL) и агента
// (normalizeFeedURL): без /Packages.gz и хвостовых слэшей.
export function normalizeFeedURL(u) {
  return String(u ?? '').replace(/\/Packages\.gz$/, '').replace(/\/+$/, '')
}

export function feedHost(u) {
  try {
    return new URL(u).hostname
  } catch {
    return String(u ?? '').slice(0, 40)
  }
}

// opkgUpgradeOutcome -- итог обновления пакетов или отключения фида. Мёртвые
// фиды приходят в payload.failed_feeds (wire.OpkgUpgradeResult) -- ровно
// откуда их брал бот, рисуя кнопку «Отключить мёртвый фид».
export function opkgUpgradeOutcome(result) {
  if (!result) return null
  const failedFeeds = (result.payload?.failed_feeds ?? []).map((raw) => {
    const url = normalizeFeedURL(raw)
    return { url, host: feedHost(url) }
  })
  const lines = String(result.output ?? '')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  let text
  if (result.status === 'locked') {
    text = MAINT_TEXTS.busy
  } else if (result.status === 'ok') {
    const report = lines.find((l) => l.startsWith('✅'))
    if (report) text = report.replace(/^✅\s*/, '')
    else if (lines[0]?.includes('уже отключён')) text = 'Этот источник пакетов уже отключён.'
    else text = 'Пакеты Entware обновлены.'
  } else if (lines.some((l) => l.includes('Не хватит места'))) {
    text = MAINT_TEXTS.noSpace
  } else {
    text = 'Не удалось обновить пакеты Entware — роутер ответил ошибкой.'
  }
  const tone = result.status === 'ok' ? (failedFeeds.length > 0 ? 'warn' : 'ok') : 'error'
  return { text, tone, failedFeeds }
}

const SERVICE_OK = {
  hrneo: 'HydraRoute Neo перезапущен.',
  hrneo_start: 'HydraRoute Neo запущен: правила по имени сайта снова работают.',
  hrneo_stop: 'HydraRoute Neo остановлен: правила по имени сайта не работают до запуска.',
  awgmgr: 'awg-manager перезапущен.',
  router: 'Роутер уходит в перезагрузку — вернётся через две–три минуты.',
}

// Отказ -- своими словами на действие: «не удалось перезапустить» на кнопке
// «Остановить» называло бы не то, что человек нажал.
const SERVICE_FAIL = {
  hrneo_start: 'Не удалось запустить HydraRoute Neo.',
  hrneo_stop: 'Не удалось остановить HydraRoute Neo.',
  router: 'Не удалось перезагрузить роутер.',
}

const SERVICE_NAME = { hrneo: 'HydraRoute Neo', awgmgr: 'awg-manager', router: 'роутер' }

export function serviceRestartText(name, result) {
  if (result?.status === 'ok') return SERVICE_OK[name] ?? 'Готово.'
  const refusal = refusalFromResult(result)
  if (refusal) return refusal.text
  if (SERVICE_FAIL[name]) return SERVICE_FAIL[name]
  return `Не удалось перезапустить ${SERVICE_NAME[name] ?? 'службу'}.`
}

// refusalFromResult -- агент отказал по своим настройкам. Строки -- из
// internal/agent/actions/runner.go (service_restart router, firmware_install).
export function refusalFromResult(result) {
  if (result?.status !== 'err') return null
  const out = String(result.output ?? '')
  if (out.includes('router reboot disabled in agent config')) return { kind: 'reboot', text: MAINT_TEXTS.rebootForbidden }
  if (out.includes('firmware install disabled in agent config')) return { kind: 'firmware', text: MAINT_TEXTS.firmwareForbidden }
  return null
}

const MAINT_ACTIONS = new Set(['awgm_update', 'hrneo_update', 'opkg_upgrade', 'opkg_feed_disable', 'service_restart', 'firmware_install'])

// maintenanceOutcomeLabel -- итог на листе для действий обслуживания; для
// остальных пусто, и лист берёт commandOutcomeLabel.
//
// status "locked" -- отдельный агентский замок на пакеты opkg (общий для
// hrneo_update и opkg_*), и агент возвращает его с английским текстом.
// Правило простое: locked НИКОГДА не показывает result.output, только
// готовую русскую фразу -- откуда бы ни пришёл замок.
export function maintenanceOutcomeLabel(action, result, args = {}) {
  if (!result || !MAINT_ACTIONS.has(action)) return ''
  if (result.status === 'locked') return MAINT_TEXTS.busy
  if (/^unknown action:/i.test(String(result.output ?? '').trim())) return AGENT_OLDER_THAN_APP
  switch (action) {
    case 'awgm_update':
      return awgmUpdateText(result)
    case 'hrneo_update':
      return hrneoUpdateText(result)
    case 'opkg_upgrade':
    case 'opkg_feed_disable':
      return opkgUpgradeOutcome(result)?.text ?? ''
    case 'service_restart':
      return serviceRestartText(args?.name, result)
    case 'firmware_install': {
      // Отказ агента по настройкам -- своей фразой; любой другой исход не
      // должен показывать сырой вывод агента (например
      // "ndmc components commit: exit status 1").
      const refusal = refusalFromResult(result)
      if (refusal) return refusal.text
      return result?.status === 'ok' ? 'Роутер ставит прошивку и перезагрузится.' : 'Не удалось поставить прошивку.'
    }
    default:
      return ''
  }
}

const ERROR_CODES = {
  agent_too_old: 'Эта кнопка заработает после обновления агента на роутере.',
  confirm_mismatch: 'Имя роутера набрано неверно — команда не отправлена.',
  reboot_cooldown: 'Роутер уже перезагружается — повторить можно через пять минут.',
}

export function commandErrorText(code) {
  return ERROR_CODES[code] ?? ''
}

// asleepNote -- сервер сообщает признак сна вместе с router_status
// (miniappWakeWindow, internal/backend/miniapp_maintenance.go): «спит» и
// «не на связи» -- разные тексты владельцу («проснётся» против «появится»),
// и решает между ними именно router_status, а не один сплющенный признак.
export function asleepNote(sent) {
  if (!sent?.router_asleep) return ''
  const minutes = sent.wake_window_min || 10
  if (sent.router_status === 'offline') {
    return `Роутер не на связи — команда выполнится, если он появится в течение ${minutes} минут.`
  }
  return `Роутер спит — команда выполнится, если он проснётся в течение ${minutes} минут.`
}

// Сколько лист ждёт итога. awgm_update опрашивает демона до 5 минут,
// hrneo_update и пакеты качают из сети; спящему роутеру -- ещё пять минут.
const LONG_MS = {
  awgm_update: 7 * 60_000,
  hrneo_update: 6 * 60_000,
  opkg_upgrade: 6 * 60_000,
  opkg_feed_disable: 6 * 60_000,
}

export function commandDeadlineMs(action, asleep) {
  const base = LONG_MS[action] ?? 90_000
  return asleep ? base + 5 * 60_000 : base
}

export function rebootBannerVisible({ versions, awgmResult }) {
  return Boolean(versions?.reboot_hint) || parseAwgmUpdate(awgmResult)?.rebootNeeded === true
}
