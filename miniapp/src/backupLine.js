// Строка «Бэкап» в карточке «Бэкенд» Парка (v0.53). Чистая функция: сервер
// отдаёт состояние ночного бэкапа в fleet.backup (причины -- готовыми
// словами из закрытого набора, без путей и сырых ошибок), экран только
// собирает из него фразу. Времена «назад» считаются по часам сервера
// (generated_at), а «сегодня/вчера» -- по часам смотрящего, как везде.
import { whenText } from './when.js'

const HOUR = 3_600_000
const DAY = 24 * HOUR
// Малый архив делается раз в сутки; сутки и два часа запаса -- ещё норма.
const STALE_AFTER = 26 * HOUR
// Прогон, начатый недавно и не завершённый, -- «идёт»; старше -- убит.
const RUNNING_FOR = 2 * HOUR

const UNITS = [
  [1024 ** 3, 'ГБ'],
  [1024 ** 2, 'МБ'],
  [1024, 'КБ'],
]

// «4 МБ», «190 МБ», «1,3 ГБ»: десятые только у малых чисел.
function sizeText(bytes) {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) return ''
  for (const [limit, suffix] of UNITS) {
    if (bytes >= limit) {
      const v = bytes / limit
      const num = v >= 10 ? String(Math.round(v)) : v.toFixed(1).replace('.', ',').replace(/,0$/, '')
      return `${num} ${suffix}`
    }
  }
  return `${bytes} Б`
}

const reasonTail = (section) => (section?.reason ? `: ${section.reason}` : '')

export function backupLine(fleet, opts = {}) {
  const b = fleet?.backup
  if (!b) return null
  if (!b.known) return { text: 'Состояние бэкапа неизвестно', tone: 'warn' }

  const now = Date.parse(fleet?.generated_at ?? '')
  const age = (iso) => {
    const t = Date.parse(iso ?? '')
    return Number.isNaN(t) || Number.isNaN(now) ? null : Math.max(0, now - t)
  }
  const small = b.small ?? {}
  const full = b.full ?? {}
  const verify = b.verify ?? {}
  const running = (s) => Boolean(s.unfinished) && (age(s.last_run_at) ?? Infinity) < RUNNING_FOR

  const problems = []

  const okAge = age(small.last_ok_at)
  const stale = okAge != null && okAge > STALE_AFTER
  const days = okAge == null ? 0 : Math.max(1, Math.floor(okAge / DAY))
  const lastGood = stale ? ` · последний удачный ${days} дн назад` : ''
  if (!running(small)) {
    if (!small.ok && small.last_run_at) {
      const word = small.telegram === 'error' && !small.unfinished ? 'Малый бэкап не ушёл в Telegram' : 'Малый бэкап не сделан'
      problems.push(`${word}${reasonTail(small)}${lastGood}`)
    } else if (!small.last_ok_at) {
      problems.push('Бэкап ещё не делался')
    } else if (stale) {
      problems.push(`Бэкап не делался ${days} дн`)
    }
  }
  if (!running(full) && !full.ok && full.last_run_at) {
    const word = full.offsite === 'error' && !full.unfinished ? 'Полный бэкап не скопирован на сервер' : 'Полный бэкап не сделан'
    problems.push(`${word}${reasonTail(full)}`)
  }
  if (verify.last_run_at && !verify.ok) {
    problems.push(`Проверка восстановления не прошла${reasonTail(verify)}`)
  }
  if (problems.length) return { text: problems.join(' · '), tone: 'danger' }

  const when = whenText(small.last_ok_at, opts).replace(', ', ' ')
  const parts = [when ? `Бэкап: ${when}` : 'Бэкап']
  const kind = (label, s, where) => {
    if (running(s)) return `${label} бэкап сейчас делается`
    return [label === 'малый' ? 'малый' : 'полный', sizeText(s.size_bytes), where].filter(Boolean).join(' ')
  }
  parts.push(kind('малый', small, small.telegram === 'ok' ? 'ушёл в Telegram' : 'на диске Pi'))
  parts.push(kind('полный', full, full.offsite === 'ok' ? 'на сервер' : 'на диске Pi'))
  if (verify.last_run_at && verify.ok) {
    const day = whenText(verify.last_run_at, opts).replace(/,? \d{2}:\d{2}$/, '')
    if (day) parts.push(`проверка восстановления ${day}`)
  }
  return { text: parts.join(' · '), tone: 'ok' }
}
