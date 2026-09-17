// Обновление агента с экрана «Парк»: что сказать в строке роутера, на листе
// подтверждения и в итоге. Здесь только чистые функции -- экран собирает из
// них строки.
//
// Решение оператора: «три необновлённых роутера выключены — нужно иметь
// возможность обновлять … в пендинг». Выключенный роутер -- не ошибка, а
// очередь: намерение живёт на сервере и поставится при выходе на связь.
// Поэтому ни одна фраза здесь не говорит «не удалось» про роутер, который
// просто спит.
//
// Отстаёт ли агент и какая оговорка у его версии, решает сервер
// (agent_behind, agent_update_warning): второе сравнение версий в клиенте
// разошлось бы с первым.
import { pluralRu } from './labels.js'

export const FLEET_UPDATE_PHRASE = 'обновить'

// Пре-флайт 15.09: статус alert перекрывает offline (dashboard_handler.go) --
// у gachimikhail «тревога» при молчании 12 суток. Не на связи = спит/офлайн,
// либо тревога, за которой на деле молчание дольше 10 минут. Для остальных
// статусов (online) возраст отчёта не проверяем: у настоящего «на связи»
// роутера он и так свежий, а проверка задним числом увела бы «онлайн» в
// «ждёт включения» на одном лишь устаревшем last_seen_age_sec.
const AWAY_STATUSES = new Set(['offline', 'sleeping'])
const AWAY_AFTER_SEC = 600

export function isAway(router) {
  // Сервер (/fleet) отдаёт away тем же правилом, по которому откладывает
  // обновление (final review M1). Порог ниже -- только для ответа без поля.
  if (typeof router?.away === 'boolean') return router.away
  if (AWAY_STATUSES.has(router?.status)) return true
  if (router?.status !== 'alert') return false
  const age = router?.last_seen_age_sec
  return typeof age === 'number' && age > AWAY_AFTER_SEC
}

function attempts(n) {
  return `${n} ${pluralRu(n, 'попытка', 'попытки', 'попыток')}`
}

export function agentUpdateState(router) {
  const pending = router?.pending_version ?? ''
  const tries = router?.pending_attempts ?? 0
  const lastError = String(router?.pending_last_error_text ?? '').trim()
  if (pending) {
    const tail = tries > 0 ? ` · ${attempts(tries)}` : ''
    // Выключенный роутер проверяется ДО причины неудачи: отметка жива, сервер
    // повторит при выходе на связь, и красное «не ставится» было бы неправдой.
    if (isAway(router)) {
      const last = lastError ? ` · прошлая попытка: ${lastError}` : ''
      return {
        tone: 'warn',
        text: `ждёт включения: ${pending} поставится, когда роутер выйдет на связь${last}`,
        canUpdate: false,
        canCancel: true,
      }
    }
    if (lastError) {
      return { tone: 'danger', text: `не ставится ${pending}: ${lastError}${tail}`, canUpdate: false, canCancel: true }
    }
    return { tone: 'muted', text: `ставится ${pending}${tail}`, canUpdate: false, canCancel: true }
  }
  if (router?.agent_behind) {
    // Отметку сервер снимает после исчерпанных попыток, а причина остаётся:
    // строка обязана её помнить, иначе роутер молча выглядит «просто
    // отстающим» и его обновляют в четвёртый раз тем же путём.
    return {
      tone: 'warn',
      text: lastError ? `не поставилось: ${lastError}` : 'агент отстаёт от бэкенда',
      canUpdate: true,
      canCancel: false,
    }
  }
  // Слишком старый агент (B6) никогда не «отстаёт» -- agent_behind у него
  // всегда false, self_update ему недоступен вовсе (см. row.warning с
  // «нужна переустановка», fleetAdmin.js). Ветку выше это обходит стороной,
  // и без этой строки прошлая (уже неактивная) попытка молча терялась бы,
  // хотя сервер её отдаёт (miniapp_fleet.go verdict.TooOld).
  if (lastError) {
    return { tone: 'warn', text: `прошлая попытка: ${lastError}`, canUpdate: false, canCancel: false }
  }
  return { tone: 'ok', text: '', canUpdate: false, canCancel: false }
}

// Коды отказа -- из ядра деплоя бэкенда. Своя фраза -- только там, где код
// однозначен. not_configured прикрывает три разные причины на сервере
// (miniapp_agent_update.go: не настроена БД, не настроена очередь, не задан
// публичный адрес) -- одна фраза здесь стёрла бы это различие, поэтому для
// него (как для bad_request и internal) используется message сервера: он
// уже по-русски и уже различает причины.
const ERROR_TEXT = {
  confirm_mismatch: 'Имя роутера набрано неверно — обновление не поставлено.',
  agent_too_old: 'Агент слишком старый, чтобы обновиться из приложения, — его нужно переустановить на роутере.',
  deploy_pending: 'Обновление этого роутера уже ждёт своей очереди — сначала отмените его.',
  // Своя версия, в том числе откат, -- отдельной кнопкой «Другая версия…».
  downgrade_rejected: 'На роутере агент новее — поставьте версию через «Другая версия…».',
  no_release: 'Не нашлось выпуска агента этой версии — обновлять не на что.',
  not_found: 'Роутер не найден — закройте экран и откройте заново.',
}

// serverMessage доверяем как есть -- только для кодов, которые пишут
// хендлеры обновления агента (miniapp_agent_update.go:63-93): их message
// всегда по-русски. Отказ может прийти и раньше, из общей middleware --
// например 401 "sign in required" на истёкшей сессии (miniapp_auth.go:198,
// review-minors-miniapp.md Important #2) -- её message английский, и без
// allowlist он утекал бы на лист как есть.
const SERVER_MESSAGE_CODES = new Set(['not_configured', 'bad_request', 'internal'])

function errorText(err) {
  const known = ERROR_TEXT[err?.code]
  if (known) return known
  if (err?.status === 401) return 'Сессия истекла — откройте приложение заново.'
  if (SERVER_MESSAGE_CODES.has(err?.code)) return String(err?.serverMessage ?? '').trim()
  return ''
}

export function agentUpdateErrorText(err) {
  return errorText(err)
}

export function agentUpdateDoneText(resp, nickname) {
  const to = resp?.target_version ?? ''
  if (resp?.deferred) return `Обновление «${nickname}» до ${to} поставится, когда роутер выйдет на связь.`
  return `Обновление «${nickname}» до ${to} отправлено на роутер. Новая версия появится после его следующего отчёта.`
}

export function agentCancelDoneText(resp, nickname) {
  if (resp?.cleared) return `Обновление «${nickname}» отменено.`
  return `Отменять было нечего: обновление «${nickname}» уже поставилось или снято.`
}

export function agentUpdateSheetText(router, backendVersion) {
  const name = router?.nickname ?? ''
  const from = router?.agent_version ?? ''
  const parts = [
    from
      ? `Агент на «${name}» обновится с ${from} до ${backendVersion}.`
      : `Агент на «${name}» обновится до ${backendVersion}.`,
  ]
  if (isAway(router)) {
    parts.push('Роутер сейчас не на связи — обновление поставится, когда он выйдет на связь.')
  }
  // Точку в конце фразы сервера срезаем: иначе вышло бы «…бэкенда..».
  const warning = String(router?.agent_update_warning ?? '').trim().replace(/[.\s]+$/, '')
  if (warning) parts.push(`Оговорка: ${warning}.`)
  parts.push('Агент перезапустится, проверки на минуту замолчат.')
  return { title: `Обновить агент на «${name}»?`, body: parts.join(' ') }
}

export function fleetUpdateTargets(fleet) {
  return (fleet?.routers ?? []).filter((r) => r?.agent_behind && !r?.pending_version)
}

export function fleetUpdateSheetText(fleet) {
  const targets = fleetUpdateTargets(fleet)
  const n = targets.length
  const names = targets.map((r) => `«${r.nickname}»`).join(', ')
  const verb = pluralRu(n, 'Отстаёт', 'Отстают', 'Отстают')
  const word = pluralRu(n, 'роутер', 'роутера', 'роутеров')
  return {
    title: 'Обновить агент на всех отставших?',
    body: `${verb} ${n} ${word}: ${names}. Выключенные и спящие получат обновление, когда выйдут на связь.`,
  }
}

export function fleetUpdateErrorText(err) {
  if (err?.code === 'confirm_mismatch') return `Слово «${FLEET_UPDATE_PHRASE}» набрано неверно — ничего не поставлено.`
  return errorText(err)
}

export function fleetUpdateSummary(results) {
  const list = results ?? []
  if (list.length === 0) return { headline: 'Отставших нет — обновлять некого.', lines: [] }
  const count = { queued: 0, deferred: 0, skipped: 0, error: 0 }
  // id -- router_id, не текст: у двух роутеров бывает одинаковый исход,
  // и ключ строки в JSX не должен от этого схлопнуться.
  const lines = []
  for (const r of list) {
    if (count[r?.outcome] != null) count[r.outcome]++
    const name = `«${r?.nickname ?? ''}»`
    const id = r?.router_id
    if (r?.outcome === 'deferred') lines.push({ id, text: `${name}: поставится, когда роутер выйдет на связь` })
    if (r?.outcome === 'skipped') lines.push({ id, text: `${name}: пропущен — ${r.reason_text || 'причина не названа'}` })
    if (r?.outcome === 'error') lines.push({ id, text: `${name}: не получилось — ${r.reason_text || 'ошибка на сервере'}` })
  }
  const parts = []
  if (count.queued) parts.push(`поставлено ${count.queued}`)
  if (count.deferred) parts.push(`${pluralRu(count.deferred, 'ждёт', 'ждут', 'ждут')} включения ${count.deferred}`)
  if (count.skipped) parts.push(`${pluralRu(count.skipped, 'пропущен', 'пропущено', 'пропущено')} ${count.skipped}`)
  if (count.error) parts.push(`не получилось ${count.error}`)
  return { headline: `Обновление: ${parts.join(', ')}.`, lines }
}
