// Строка «Сторож» в Парке и строки отложенного в строке роутера. Только
// чистые функции -- экран собирает из них текст.
//
// Сторож -- тот, кто пишет «роутер не на связи». Он молчит в двух случаях:
// когда в парке всё хорошо и когда он сам мёртв. Строка обязана эту разницу
// показать: когда был последний обход и сколько роутеров он сейчас держит
// молчащими и заглушёнными.
import { humanAge, pluralRu } from './labels.js'
import { secondsBetween, stampText } from './stamp.js'

function ago(sec) {
  return sec < 60 ? `${sec} с` : humanAge(sec)
}

const num = (v) => typeof v === 'number' && Number.isFinite(v)

export function watchdogLine(fleet) {
  const wd = fleet?.watchdog
  if (!wd) return null
  const parts = []
  const sec = secondsBetween(wd.last_scan_at, fleet?.generated_at)
  parts.push(sec == null ? 'обхода ещё не было' : `последний обход ${ago(sec)} назад`)
  // Старый бэкенд счётчиков не отдаёт: «молчат 0» было бы выдумкой.
  if (num(wd.stale_users)) parts.push(`молчат ${wd.stale_users}`)
  if (num(wd.suppressed_users)) parts.push(`заглушено ${wd.suppressed_users}`)

  const sub = []
  if (num(wd.scans_total) && wd.scans_total > 0) {
    sub.push(`${wd.scans_total} ${pluralRu(wd.scans_total, 'обход', 'обхода', 'обходов')} с запуска`)
  }
  if (num(wd.last_scan_ms) && wd.last_scan_ms > 0) sub.push(`обход занял ${wd.last_scan_ms} мс`)
  const errors = num(wd.offline_errors) ? wd.offline_errors : 0
  if (errors > 0) {
    sub.push(`${errors} ${pluralRu(errors, 'отправка не ушла', 'отправки не ушли', 'отправок не ушло')}`)
  }

  const dead = wd.alive === false
  return {
    text: parts.join(' · '),
    sub: sub.join(' · '),
    tone: dead ? 'danger' : errors > 0 ? 'warn' : 'ok',
    alarm: dead ? String(wd.reason ?? '').trim() || 'сторож не обходит парк' : '',
  }
}

export function routerDelayLines(router, opts = {}) {
  const lines = []
  const since = stampText(router?.pending_since, opts)
  if (since) lines.push({ key: 'pending', tone: 'muted', text: `ждёт обновления с ${since}` })

  const deploy = router?.last_deploy
  if (deploy) {
    const verdict = deploy.ok === true ? 'прошла' : deploy.ok === false ? 'не прошла' : ''
    const head = deploy.version ? `последняя раскатка ${deploy.version}` : 'последняя раскатка'
    const text = [head, stampText(deploy.at, opts), verdict].filter(Boolean).join(' · ')
    lines.push({ key: 'deploy', tone: deploy.ok === true ? 'ok' : deploy.ok === false ? 'danger' : 'muted', text })
  }

  const incident = router?.incident
  const hard = stampText(incident?.hard_since, opts)
  if (hard) {
    const n = num(incident.fail_count) ? incident.fail_count : 0
    const times = n > 0 ? ` (${n} ${pluralRu(n, 'раз', 'раза', 'раз')})` : ''
    lines.push({ key: 'incident', tone: 'danger', text: `тревога с ${hard}${times}` })
  }
  return lines
}
