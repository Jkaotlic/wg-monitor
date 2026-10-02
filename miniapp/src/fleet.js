// Экран флота: сломанное сверху, состояние -- словами.
//
// Пять точек отсюда удалены вместе с легендой под списком. Легенда и была
// доказательством, что прибор не читается: если под точками приходится
// писать, что значит серая, значит точки не работают.
//
// Список открывают, когда что-то сломалось, поэтому порядок -- по срочности,
// а строка отвечает не «сколько тревог», а «что именно не так»: число
// человеку ничего не говорит, фраза говорит.
import { humanAge, incidentWhatPlain, pluralRu } from './labels.js'
import { agoText } from './when.js'
import { isStale, reachStatus } from './staleness.js'

// Порядок -- по срочности, а не по id.
const URGENCY = { alert: 0, offline: 1, sleeping: 2, online: 3 }

export function sortByUrgency(routers = []) {
  return [...routers].sort((a, b) => {
    // Молчащая тревога -- среди молчащих: пока нет связи, чинить её нечем.
    const ua = URGENCY[reachStatus(a)] ?? 99
    const ub = URGENCY[reachStatus(b)] ?? 99
    if (ua !== ub) return ua - ub
    return (a.nickname ?? '').localeCompare(b.nickname ?? '', 'ru')
  })
}

// Красная тревога -- единственный предикат для Парка, полосы выбора и
// главного экрана: тревога есть, не только по запасному VPN-туннелю
// (reserve_only_alert) и роутер не молчит -- у молчащего чинить нечем.
export function redAlert(router) {
  return reachStatus(router) === 'alert' && !router?.reserve_only_alert
}

export function fleetRow(router) {
  const age = router?.last_seen_age_sec
  const incidents = router?.active_incidents ?? []
  const never = age == null
  // Молчание перебивает тревогу: у роутера, который не отчитывается, тревога
  // -- вчерашняя новость (staleness.js).
  const status = reachStatus(router)

  let pill
  if (never) {
    pill = { tone: 'muted', text: 'ни разу не отвечал' }
  } else if (status === 'alert' && router.reserve_only_alert) {
    // Все тревоги -- по запасным звеньям, несущий жив: обход работает. Красное
    // «тревога» тут гнало бы чинить срочно то, что можно чинить спокойно.
    // Сервер знает несущего, поэтому признак считает он.
    pill = { tone: 'warn', text: 'резерв не работает' }
  } else if (status === 'alert') {
    pill = { tone: 'danger', text: 'тревога' }
  } else if (status === 'offline') {
    pill = { tone: 'danger', text: `молчит ${humanAge(age)}` }
  } else if (status === 'sleeping') {
    pill = { tone: 'warn', text: `спит ${humanAge(age)}` }
  } else {
    pill = { tone: 'ok', text: 'в порядке' }
  }

  let sub
  if (incidents.length > 0) {
    // Идентификатор VPN-туннеля в этом списке не значит ничего: у человека здесь
    // нет ни снимка маршрутов, ни имён туннелей, чтобы понять, что такое
    // awg12. Внутри роутера VPN-туннель назван именем -- туда и идти.
    sub = router.reserve_only_alert
      ? 'запасной VPN-туннель не отвечает, обход работает'
      : incidentWhatPlain(incidents[0].check_name)
  } else if (never) {
    sub = 'агент установлен, но отчётов от него не было'
  } else {
    sub = `отчёт ${agoText(age)}`
  }

  return { id: router?.id, nickname: router?.nickname ?? '', pill, sub, panelURL: router?.panel_url ?? '' }
}

// Групповой опрос флота. Одна кнопка, N роутеров -- и человек обязан видеть,
// чем кончилось у каждого: «готово» на группе, где двое не ответили, это
// ровно то враньё, против которого написано остальное приложение.
export function batchProgress(state) {
  if (!state || !state.total) return ''
  const { total, ok, failed, done } = state
  if (done < total) return `Ответил${ok === 1 ? '' : 'и'} ${ok} из ${total}…`
  if (failed === 0) return `Опрошены все ${total}`
  return `Ответили ${ok} из ${total}, не ответил${failed === 1 ? '' : 'и'} ${failed}`
}

// Сводка парка для широкого экрана, когда роутер не выбран: три числа и
// карточки того, что требует рук. Молчащий -- отдельно от тревоги: у
// тревоги есть что чинить, у молчащего сначала надо вернуть связь.
function bucket(router) {
  if (router?.last_seen_age_sec == null) return 'silent'
  if (isStale(router)) return 'silent'
  if (router.status === 'alert') return 'attention'
  return 'ok'
}

export function fleetSummary(routers = []) {
  const sorted = sortByUrgency(routers)
  const out = { total: sorted.length, ok: 0, attention: 0, silent: 0, broken: [] }
  for (const r of sorted) {
    const b = bucket(r)
    out[b]++
    if (b !== 'ok') out.broken.push(fleetRow(r))
  }
  return out
}

// Один словарь состояний парка (v0.50, спека п. 2.3): «в порядке»,
// «тревога», «молчит» -- в плитках сводки, в строке «Мои роутеры», на
// карточках Парка. Число с согласованием: «1 молчит», «2 молчат».
export function stateCountLabel(kind, n) {
  if (kind === 'ok') return 'в порядке'
  if (kind === 'attention') return pluralRu(n, 'тревога', 'тревоги', 'тревог')
  return pluralRu(n, 'молчит', 'молчат', 'молчат')
}

// Строка сводки -- одна на «Мои роутеры» и сводку широкого экрана.
export function fleetSummaryLine(s) {
  const total = s?.total ?? 0
  if (!total) return 'Роутеров пока нет.'
  if (!s.attention && !s.silent) return total === 1 ? 'Роутер в порядке.' : `Все ${total} в порядке.`
  const parts = []
  if (s.attention) parts.push(`${s.attention} ${stateCountLabel('attention', s.attention)}`)
  if (s.silent) parts.push(`${s.silent} ${stateCountLabel('silent', s.silent)}`)
  if (s.ok) parts.push(`${s.ok} ${stateCountLabel('ok', s.ok)}`)
  return `${total} ${pluralRu(total, 'роутер', 'роутера', 'роутеров')}: ${parts.join(', ')}.`
}

// Лист быстрого выбора роутера из шапки (спека п. 2.6): по срочности, с той
// же пилюлей, что в списке; поиск -- только когда роутеров больше шести.
export function routerSwitchChoices(routers = [], currentID = null) {
  const choices = sortByUrgency(routers).map((r) => {
    const row = fleetRow(r)
    return { value: r.id, label: row.nickname, pill: row.pill, current: r.id === currentID }
  })
  return { choices, search: routers.length > 6 }
}
