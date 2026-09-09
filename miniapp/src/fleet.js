// Экран флота: сломанное сверху, состояние -- словами.
//
// Пять точек отсюда удалены вместе с легендой под списком. Легенда и была
// доказательством, что прибор не читается: если под точками приходится
// писать, что значит серая, значит точки не работают.
//
// Список открывают, когда что-то сломалось, поэтому порядок -- по срочности,
// а строка отвечает не «сколько тревог», а «что именно не так»: число
// человеку ничего не говорит, фраза говорит.
import { humanAge, incidentWhatPlain } from './labels.js'

// Порядок -- по срочности, а не по id.
const URGENCY = { alert: 0, offline: 1, sleeping: 2, online: 3 }

export function sortByUrgency(routers = []) {
  return [...routers].sort((a, b) => {
    const ua = URGENCY[a.status] ?? 99
    const ub = URGENCY[b.status] ?? 99
    if (ua !== ub) return ua - ub
    return (a.nickname ?? '').localeCompare(b.nickname ?? '', 'ru')
  })
}

export function fleetRow(router) {
  const age = router?.last_seen_age_sec
  const incidents = router?.active_incidents ?? []
  const never = age == null

  let pill
  if (never) {
    pill = { tone: 'muted', text: 'ни разу не отвечал' }
  } else if (router.status === 'alert') {
    pill = { tone: 'danger', text: 'тревога' }
  } else if (router.status === 'offline') {
    pill = { tone: 'danger', text: `нет ответа ${humanAge(age)}` }
  } else if (router.status === 'sleeping') {
    pill = { tone: 'warn', text: `спит ${humanAge(age)}` }
  } else {
    pill = { tone: 'ok', text: 'в порядке' }
  }

  let sub
  if (incidents.length > 0) {
    // Идентификатор линии в этом списке не значит ничего: у человека здесь
    // нет ни снимка маршрутов, ни имён туннелей, чтобы понять, что такое
    // awg12. Внутри роутера линия названа именем -- туда и идти.
    sub = incidentWhatPlain(incidents[0].check_name)
  } else if (never) {
    sub = 'агент установлен, но отчётов от него не было'
  } else {
    sub = `отчёт ${humanAge(age)} назад`
  }

  return { id: router?.id, nickname: router?.nickname ?? '', pill, sub }
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
