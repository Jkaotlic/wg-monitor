// Массовые проверки на экране «Парк»: «Проверить все» и «Аудит всех».
//
// Это N обычных команд, а не новая власть над парком: каждая идёт по своему
// роутеру через тот же белый список и ту же проверку доступа, что кнопка на
// экране одного роутера (образец -- FleetOverlay.recheckAll). Поэтому здесь
// нет ни своего маршрута, ни своих прав -- только цикл и честный счёт.
//
// Спящие и выключенные не опрашиваются: команда подождала бы их и отменилась,
// а экран до конца ожидания показывал бы «проверяем». Их итог называет по
// имени -- пропуск молча был бы враньём «проверено всё».
import { doctorRows, auditRows } from './settings.js'
import { pluralRu } from './labels.js'
import { isAway } from './agentUpdate.js'

export const BATCH = {
  doctor: { action: 'router_doctor', idle: 'Проверить все', busy: 'Проверяем…' },
  audit: { action: 'version_audit', idle: 'Аудит всех', busy: 'Сверяем версии…' },
}

export function batchTargets(routers) {
  const targets = []
  const skipped = []
  for (const r of routers ?? []) (isAway(r) ? skipped : targets).push(r)
  return { targets, skipped }
}

// Подробности осмотра агент пишет по-английски и именами проверок
// («pingcheck: disabled»). В сводку парка они не идут: итог говорит числа, а
// что именно не так -- экран роутера, где ответ разобран и есть целиком.
export function doctorProblems(output) {
  const rows = doctorRows(output)
  if (rows.length === 0) return null
  return {
    fails: rows.filter((r) => r.tone === 'danger').length,
    warns: rows.filter((r) => r.tone === 'warn').length,
  }
}

export function auditProblems(output) {
  const rows = auditRows(output)
  if (rows.length === 0) return null
  return rows.filter((r) => r.tone !== 'ok').map((r) => `${r.title} — ${r.sub}`)
}

const PARSE = { doctor: doctorProblems, audit: auditProblems }

function hasProblems(kind, problems) {
  if (kind === 'doctor') return problems.fails + problems.warns > 0
  return problems.length > 0
}

export async function runFleetBatch({ kind, routers, send, poll, onProgress = () => {}, deadlineMs = 90_000, now = () => Date.now() }) {
  const { action } = BATCH[kind]
  const parse = PARSE[kind]
  const { targets, skipped } = batchTargets(routers)
  const state = {
    kind,
    total: targets.length,
    done: 0,
    running: targets.length > 0,
    skipped: skipped.map((r) => r.nickname),
    results: [],
  }
  const snapshot = () => ({ ...state, results: [...state.results] })
  onProgress(snapshot())

  await Promise.all(
    targets.map(async (r) => {
      let entry = { nickname: r.nickname, outcome: 'no_answer' }
      try {
        const { cmd_id: id } = await send(r.id, action, {})
        const until = now() + deadlineMs
        let res = null
        while (now() < until) {
          res = await poll(r.id, id, 10)
          if (res) break
        }
        if (res && res.status !== 'ok') {
          entry = { nickname: r.nickname, outcome: 'failed' }
        } else if (res) {
          const problems = parse(res.output)
          if (problems === null) entry = { nickname: r.nickname, outcome: 'unparsed' }
          else if (hasProblems(kind, problems)) entry = { nickname: r.nickname, outcome: 'problems', problems }
          else entry = { nickname: r.nickname, outcome: 'ok' }
        }
      } catch {
        entry = { nickname: r.nickname, outcome: 'no_answer' }
      }
      state.results.push(entry)
      state.done += 1
      state.running = state.done < state.total
      onProgress(snapshot())
    }),
  )
  return snapshot()
}

export function batchProgressLine(state) {
  if (!state || !state.running) return ''
  return `${pluralRu(state.done, 'Ответил', 'Ответили', 'Ответили')} ${state.done} из ${state.total}…`
}

function doctorCounts({ fails, warns }) {
  const parts = []
  if (fails) parts.push(`${fails} ${pluralRu(fails, 'сбой', 'сбоя', 'сбоев')}`)
  if (warns) parts.push(`${warns} ${pluralRu(warns, 'замечание', 'замечания', 'замечаний')}`)
  return parts.join(', ')
}

const OUTCOME_LINE = {
  unparsed: 'ответ не разобран',
  failed: 'команда не выполнилась',
  no_answer: 'не ответил',
}

export function batchSummary(state) {
  const kind = state?.kind ?? 'doctor'
  const results = state?.results ?? []
  const skipped = state?.skipped ?? []
  if (!state?.total) {
    return {
      headline: kind === 'doctor' ? 'Все роутеры не на связи — проверять некого.' : 'Все роутеры не на связи — сверять некого.',
      lines: skipped.length ? [skippedLine(skipped)] : [],
    }
  }
  const answered = results.filter((r) => r.outcome === 'ok' || r.outcome === 'problems' || r.outcome === 'unparsed').length
  const withProblems = results.filter((r) => r.outcome === 'problems').length

  let headline
  if (kind === 'doctor') {
    headline = `Проверено ${answered} из ${state.total}, ${withProblems ? `проблемы у ${withProblems}` : 'проблем не нашлось'}.`
  } else {
    const tail = withProblems
      ? `внимания ${pluralRu(withProblems, 'требует', 'требуют', 'требуют')} ${withProblems}`
      : 'всё свежее и работает'
    headline = `Аудит: ответили ${answered} из ${state.total}, ${tail}.`
  }

  const lines = [...results]
    .filter((r) => r.outcome !== 'ok')
    .sort((a, b) => a.nickname.localeCompare(b.nickname, 'ru'))
    .map((r) => {
      if (r.outcome !== 'problems') return `«${r.nickname}»: ${OUTCOME_LINE[r.outcome]}`
      return `«${r.nickname}»: ${kind === 'doctor' ? doctorCounts(r.problems) : r.problems.join('; ')}`
    })
  if (skipped.length) lines.push(skippedLine(skipped))
  if (kind === 'doctor' && withProblems) {
    lines.push('Что именно не так — на экране роутера: «Настройки» → «Осмотр роутера».')
  }
  return { headline, lines }
}

function skippedLine(names) {
  return `Не на связи, ${pluralRu(names.length, 'пропущен', 'пропущены', 'пропущены')}: ${names.map((n) => `«${n}»`).join(', ')}.`
}
