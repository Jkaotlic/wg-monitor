// Слово awg-manager о VPN-туннеле -- то, что его UI пишет на карточке туннеля
// (v0.65). Оператор хочет видеть его рядом с вердиктом бота: они расходятся
// (router4car4 02.11: роутер писал «Нет связи», бот -- «всё ок»), и
// расхождение должно быть видно сразу, а не после поездки в веб-интерфейс.
//
// Правило снято с бандла UI awg-manager 2.19.x (спека v0.65), первое
// совпавшее: «Конфликт IP» -> «Нет связи» по проверке связи -> слово по
// статусу. Считается здесь, а не на агенте: правится раскаткой бэкенда, без
// обновления агентов.
//
// t -- строка events.tunnels (проекция проверки: run_state, address_conflict,
// ping_check_status, awgm_*), НЕ снимок маршрутов: туда withCheckVerdict пишет
// свой «dead», и слово awg-manager подменилось бы мнением бота.
//
// tone -- смысл цвета, не класс: ok | warn | bad | muted.

const BY_STATUS = {
  broken: { label: 'Сломан', tone: 'bad' },
  starting: { label: 'Запускается', tone: 'warn' },
  needs_stop: { label: 'Останавливается', tone: 'warn' },
  stopping: { label: 'Останавливается', tone: 'warn' },
  needs_start: { label: 'Остановлен', tone: 'muted' },
  stopped: { label: 'Остановлен', tone: 'muted' },
  not_created: { label: 'Не создан', tone: 'muted' },
  disabled: { label: 'Выключен', tone: 'muted' },
}

export function awgmBadge(t) {
  const status = typeof t?.run_state === 'string' ? t.run_state.trim() : ''
  if (!status) return null
  const reason = (t.awgm_conn_reason ?? '').trim()
  if (t.address_conflict === true) return { label: 'Конфликт IP', tone: 'bad', note: '' }
  if (status === 'running') {
    // UI awg-manager пишет «Нет связи» только у running: у остальных ответ о
    // связи слова не меняет.
    if (t.awgm_conn === 'disconnected') return { label: 'Нет связи', tone: 'bad', note: reason }
    if (t.ping_check_status === 'recovering') return { label: 'Восстанавливается', tone: 'warn', note: '' }
    return { label: 'Активен', tone: 'ok', note: activeNote(t) }
  }
  const known = BY_STATUS[status]
  if (!known) return { label: status, tone: 'muted', note: '' }
  // Пояснение awg-manager к broken («Не запустился — перезапустите туннель»)
  // UI показывает прямо на карточке -- и мы так же.
  const note = status === 'broken' ? (t.awgm_status_details ?? '').trim() : ''
  return { ...known, note }
}

// Пояснение к «Активен»: молча «Активен» без ответа о связи было бы
// обещанием, которого никто не проверял. Сбой вопроса -- не «Нет связи»:
// awg-manager просто ничего не сказал.
function activeNote(t) {
  switch (t.awgm_conn) {
    case 'connected':
      return Number.isFinite(t.awgm_conn_latency_ms) ? `${t.awgm_conn_latency_ms} мс` : ''
    case 'error':
      return 'awg-manager не ответил на проверку связи'
    default:
      return 'связь не проверялась'
  }
}

// Класс тона экрана для смысла цвета: свои цвета строке не нужны, у экрана
// они уже есть (data-row-dot-*, data-row-value-*).
const TONE_CLASS = { ok: 'ok', warn: 'warn', bad: 'danger', muted: 'muted' }

export function awgmToneClass(tone) {
  return TONE_CLASS[tone] ?? 'muted'
}
