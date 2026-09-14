// Сброс DNS: чистые функции экрана отдельно от вёрстки.
//
// Самое опасное действие цикла v0.31, поэтому запреты здесь важнее вёрстки:
//
//   1. Экран для роутера с агентом ниже пола версии не рисуется вовсе -- это
//      ВТОРАЯ преграда, независимая от гейта бэкенда
//      (miniappActionMinAgentVersion). Старый агент не знает dry_run и на
//      «посмотреть, что изменится» сделал бы настоящий сброс.
//   2. Кнопка сброса неактивна, пока не отработал предпросмотр.
//   3. Экран не обещает того, чего нет: снимок «до» -- файл на роутере, по
//      которому настройки возвращают руками. Кнопки «вернуть» нет, и слов о
//      ней тоже.
//   4. Постусловия читаются только из СВЕЖЕГО отчёта: пока роутер не прислал
//      новый, ответ «ждём», а не прежнее «да».

import { agentAtLeast } from './agentConfig.js'
import { guardVerdict } from './labels.js'

export const DNS_RESET_MIN_VERSION = 'v0.31.0'

const TEXTS = {
  intro:
    'Сброс заменит DNS-серверы роутера эталонными: русские зоны — Яндексу по защищённому соединению, остальное — заграничным серверам. Свой DNS-сервер останется.',
  previewButton: 'Посмотреть, что изменится',
  resetButton: 'Сбросить DNS',
  previewFirst: 'Сначала посмотрите, что изменится: без этого сбросить нельзя.',
  tooOld: 'Сброс DNS появится после обновления агента на роутере.',
  adminOnly: 'DNS роутера сбрасывает админ бота.',
  confirmTail: 'Роутер заменит свои DNS-серверы эталонными и сохранит настройки.',
  waiting: 'ждём отчёта',
  checkAgain: 'Проверить ещё раз',
}

export function dnsResetScreenTexts() {
  return { ...TEXTS }
}

// Кому экран доступен: только админ бота и только агент от пола версии.
// Отказ по умолчанию: пустая, нечитаемая и предрелизная версия ЗАПРЕЩАЮТ.
export function dnsResetAvailable(settings) {
  if (!settings || settings.role !== 'admin') return false
  return agentAtLeast(settings.agent_version, DNS_RESET_MIN_VERSION)
}

export function resetEnabled({ previewed } = {}) {
  return previewed === true
}

export function dnsResetConfirmBody(routerName) {
  return `Наберите имя роутера «${routerName}», чтобы подтвердить сброс DNS. ${TEXTS.confirmTail}`
}

// parsePreview разбирает ответ агента на dry_run. Не предпросмотр -- null:
// ответ настоящего сброса за предпросмотр не выдаётся ни при каком разборе.
export function parsePreview(output) {
  const text = String(output ?? '')
  if (!text.startsWith('Предпросмотр')) return null
  const out = { remove: [], keep: [], addCount: 0 }
  let section = ''
  for (const raw of text.split('\n')) {
    const add = /^Заменим на эталонные \((\d+)\)/.exec(raw)
    if (add) out.addCount = Number(add[1])
    if (raw.startsWith('Уберём')) section = 'remove'
    else if (raw.startsWith('Оставим')) section = 'keep'
    else if (raw.startsWith('Заменим')) section = 'add'
    const line = /^\s+[−=+]\s+(.+)$/.exec(raw)
    if (line && (section === 'remove' || section === 'keep')) out[section].push(line[1])
  }
  return out
}

function plural(n, one, few, many) {
  const m10 = n % 10
  const m100 = n % 100
  if (m10 === 1 && m100 !== 11) return one
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few
  return many
}

export function previewText(p) {
  const now = p.remove.length + p.keep.length
  let text = `Сейчас на роутере ${now} ${plural(now, 'строка', 'строки', 'строк')} DNS. Заменим на эталонные: ${p.addCount}.`
  if (p.keep.length > 0) text += ' Свой DNS-сервер не тронем.'
  return text
}

const SNAPSHOT = /снимок «до»: (\S+)/
const SNAPSHOT_FAILED = /снимок «до» не записан/

export function parseReset(result) {
  const output = String(result?.output ?? '')
  const m = SNAPSHOT.exec(output)
  return {
    status: result?.status ?? '',
    snapshot: m ? m[1] : '',
    snapshotFailed: SNAPSHOT_FAILED.test(output),
  }
}

export function doneText(r) {
  if (r.status === 'err') return 'Роутер не сбросил DNS: не смог прочитать свои настройки. Ничего не изменилось.'
  const saved = r.snapshot
    ? `Прежние настройки DNS сохранены на роутере в файле ${r.snapshot} — по нему их можно вернуть руками.`
    : ''
  if (r.status === 'partial') {
    return `Сброс прошёл не целиком: часть команд роутер не принял.${saved ? ' ' + saved : ''}`
  }
  if (!saved) return 'Готово, но прежние настройки сохранить не удалось — вернуть их будет не по чему.'
  return `Готово. ${saved}`
}

function find(checks, name) {
  return (checks ?? []).find((c) => c.check_name === name) ?? null
}

// Свежесть -- по меткам самого роутера до и после, а не по часам телефона:
// часы роутера и телефона вправе расходиться, а сравнение двух меток одного
// источника от этого не зависит.
function fresh(beforeCheck, afterCheck, stamp) {
  if (!afterCheck) return false
  if (!beforeCheck) return true
  return stamp(afterCheck) !== stamp(beforeCheck)
}

// postconditionRows -- три вопроса после сброса: резолвит ли роутер, остался
// ли свой DNS-сервер у сторожа, у Яндекса ли русские зоны.
export function postconditionRows({ before, after }) {
  const splitStamp = (c) => c.details?.checked_at ?? c.ts
  const splitBefore = find(before, 'dns_split')
  const split = find(after, 'dns_split')
  const splitFresh = fresh(splitBefore, split, splitStamp)

  const rows = []
  const d = split?.details ?? {}
  rows.push({
    key: 'resolves',
    title: 'Роутер отвечает на запросы имён сайтов',
    ...(!splitFresh
      ? { value: TEXTS.waiting, tone: 'muted' }
      : d.resolves === 'ok'
        ? { value: 'да', tone: 'ok' }
        : { value: 'нет', tone: 'danger' }),
  })

  const guardBefore = find(before, 'resolver_guard')
  const guard = find(after, 'resolver_guard')
  let guardRow
  // Нет строки resolver_guard в свежем отчёте -- сторож выключен. Было ли что
  // до сброса, не важно: после Fix 2 «мини-апп не показывает последнюю строку
  // остановленного сторожа» у выключенного сторожа строки просто не будет,
  // и это тот же самый «выключен», а не поломка.
  if (!guard) guardRow = { value: 'сторож выключен', tone: 'muted' }
  else if (!fresh(guardBefore, guard, (c) => c.ts)) guardRow = { value: TEXTS.waiting, tone: 'muted' }
  else {
    // Один словарь исходов сторожа на всё приложение (guardVerdict,
    // labels.js) -- раньше idle/ready читались здесь отдельной копией
    // правила и разошлись бы с экраном диагностики при первой же правке.
    switch (guardVerdict(guard)) {
      case 'ok':
        guardRow = { value: 'да', tone: 'ok' }
        break
      case 'idle':
        guardRow = { value: 'нет — сторож его потерял', tone: 'danger' }
        break
      case 'unread':
        guardRow = { value: 'ещё не прочитал настройки', tone: 'muted' }
        break
      case 'fallback':
        guardRow = { value: 'на запасных', tone: 'warn' }
        break
      case 'leftover':
        guardRow = { value: 'запасные рядом', tone: 'warn' }
        break
      case 'down':
        guardRow = { value: 'нет', tone: 'danger' }
        break
      default:
        guardRow = { value: TEXTS.waiting, tone: 'muted' }
    }
  }
  rows.push({ key: 'guard', title: 'Свой DNS-сервер остался на месте', ...guardRow })

  const zones = Object.values(d.zones ?? {})
  let zonesRow
  if (!splitFresh) zonesRow = { value: TEXTS.waiting, tone: 'muted' }
  else if (zones.length === 0 || zones.every((v) => v === 'unknown')) zonesRow = { value: 'неизвестно по всем зонам', tone: 'warn' }
  else {
    const onYandex = zones.filter((v) => v === 'yandex_dot').length
    zonesRow = { value: `${onYandex} из ${zones.length}`, tone: onYandex === zones.length ? 'ok' : 'warn' }
  }
  rows.push({ key: 'zones', title: 'Русские зоны у Яндекса', ...zonesRow })
  return rows
}
