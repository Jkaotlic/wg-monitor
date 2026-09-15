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

// Опрашивать не больше MAX_CONCURRENT роутеров разом: пачка на весь парк
// (десятки роутеров) не должна бить по awg-manager и очереди команд одним
// залпом. Место освобождается по одному -- следующий роутер стартует и
// получает свой полный дедлайн от СВОЕГО старта, а не от начала пачки.
const MAX_CONCURRENT = 3

// Пул с ограничением: `limit` воркеров тянут задачи по очереди, следующая
// начинается только когда предыдущая в этом воркере закончилась. `signal`
// (необязательный) даёт экрану оборвать цикл: воркер проверяет его перед
// каждой новой задачей и просто останавливается -- задачи, уже начатые,
// доходят до конца, а очередь дальше не идёт.
async function runPool(items, limit, worker, signal) {
  let idx = 0
  async function next() {
    while (idx < items.length) {
      if (signal?.cancelled) return
      const i = idx++
      await worker(items[i])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, next))
}

export async function runFleetBatch({
  kind,
  routers,
  send,
  poll,
  onProgress = () => {},
  deadlineMs = 90_000,
  now = () => Date.now(),
  concurrency = MAX_CONCURRENT,
  signal,
}) {
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

  await runPool(
    targets,
    concurrency,
    async (r) => {
      let entry = { id: r.id, nickname: r.nickname, outcome: 'no_answer' }
      try {
        const { cmd_id: id } = await send(r.id, action, {})
        // Дедлайн -- от этого момента: роутер, простоявший в очереди пула,
        // получает полные deadlineMs с СВОЕГО старта, а не остаток чужого.
        const until = now() + deadlineMs
        let res = null
        // signal?.cancelled -- иначе экран ушёл, а уже опрашиваемый роутер
        // долбит poll() дальше до своего 90с дедлайна (Fix round 1, review
        // Important #1): runPool останавливает только ОЧЕРЕДЬ, не активный опрос.
        while (!signal?.cancelled && now() < until) {
          res = await poll(r.id, id, 10)
          if (res) break
        }
        // Дальше результат не обрабатываем -- экран, ради которого опрашивали,
        // уже ушёл: даже свежий res, подоспевший ровно на отмене, никому не нужен.
        if (signal?.cancelled) return
        if (res && res.status !== 'ok') {
          entry = { id: r.id, nickname: r.nickname, outcome: 'failed' }
        } else if (res) {
          const problems = parse(res.output)
          if (problems === null) entry = { id: r.id, nickname: r.nickname, outcome: 'unparsed' }
          else if (hasProblems(kind, problems)) entry = { id: r.id, nickname: r.nickname, outcome: 'problems', problems }
          else entry = { id: r.id, nickname: r.nickname, outcome: 'ok' }
        }
      } catch {
        entry = { id: r.id, nickname: r.nickname, outcome: 'no_answer' }
      }
      state.results.push(entry)
      state.done += 1
      state.running = state.done < state.total
      onProgress(snapshot())
    },
    signal,
  )
  return snapshot()
}

// «Ответили» -- реальный ответ роутера (ok/problems/unparsed), не число
// завершённых попыток: таймаут и отказ команды уже случились, но роутер не
// ответил, и считать их «ответом» здесь означало бы врать раньше времени
// то же самое, что итог (batchSummary) скажет в конце.
function answeredCount(results) {
  return results.filter((r) => r.outcome === 'ok' || r.outcome === 'problems' || r.outcome === 'unparsed').length
}

function timeoutCount(results) {
  return results.filter((r) => r.outcome === 'no_answer').length
}

function errorCount(results) {
  return results.filter((r) => r.outcome === 'failed').length
}

// F1(d) дословно: прогресс и итог называют «ответили» ОДНИМ И ТЕМ ЖЕ числом
// (реальные ответы -- answeredCount, как в batchSummary), а таймауты и
// отказы -- отдельными словами, а не растворены в «ответили». «Готово N из
// M» -- общий ход (число двигается вместе с пулом, не выглядит зависшим:
// review-minors-miniapp.md Minor #3), а «ответили»/«молчат»/«отказ» --
// подробность через двоеточие, нулевые части опускаются.
export function batchProgressLine(state) {
  if (!state || !state.running) return ''
  const results = state.results ?? []
  const answered = answeredCount(results)
  const timeouts = timeoutCount(results)
  const errors = errorCount(results)
  const finished = answered + timeouts + errors
  const parts = []
  if (answered) parts.push(`${pluralRu(answered, 'ответил', 'ответили', 'ответили')} ${answered}`)
  if (timeouts) parts.push(`${pluralRu(timeouts, 'молчит', 'молчат', 'молчат')} ${timeouts}`)
  if (errors) parts.push(`${pluralRu(errors, 'отказ', 'отказа', 'отказов')} ${errors}`)
  const detail = parts.length ? `: ${parts.join(', ')}` : ''
  return `Готово ${finished} из ${state.total}${detail}…`
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
      lines: skipped.length ? [{ id: 'skipped', text: skippedLine(skipped) }] : [],
    }
  }
  const answered = answeredCount(results)
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

  // id -- router_id, не текст: у двух роутеров бывает одинаковый исход
  // («не ответил»), и ключ строки в JSX не должен от этого схлопнуться.
  const lines = [...results]
    .filter((r) => r.outcome !== 'ok')
    .sort((a, b) => a.nickname.localeCompare(b.nickname, 'ru'))
    .map((r) => ({
      id: r.id,
      text:
        r.outcome !== 'problems'
          ? `«${r.nickname}»: ${OUTCOME_LINE[r.outcome]}`
          : `«${r.nickname}»: ${kind === 'doctor' ? doctorCounts(r.problems) : r.problems.join('; ')}`,
    }))
  if (skipped.length) lines.push({ id: 'skipped', text: skippedLine(skipped) })
  if (kind === 'doctor' && withProblems) {
    lines.push({ id: 'doctor-note', text: 'Что именно не так — на экране роутера: «Настройки» → «Осмотр роутера».' })
  }
  return { headline, lines }
}

function skippedLine(names) {
  return `Не на связи, ${pluralRu(names.length, 'пропущен', 'пропущены', 'пропущены')}: ${names.map((n) => `«${n}»`).join(', ')}.`
}
