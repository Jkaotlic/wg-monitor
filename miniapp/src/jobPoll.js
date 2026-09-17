// Опрос задания: машина состояний и последовательный цикл. Цикл, а не
// setInterval: медленный ответ не наслаивается на следующий запрос, а тест
// подменяет sleep и не ждёт настоящих секунд.
//
// Фазы: loading (ещё ни одного ответа), live (есть задание), expired (404 --
// сервер забыл задание через 30 минут после конца), lost (40 сетевых ошибок
// подряд -- около минуты без связи; задание могло продолжиться).

export const JOB_POLL_MS = 1500
export const JOB_POLL_FAILURE_LIMIT = 40

export function jobPollStart() {
  return { phase: 'loading', job: null, failures: 0 }
}

function terminal(state) {
  return state.phase === 'expired' || state.phase === 'lost'
}

export function jobPollStep(state, event) {
  if (terminal(state)) return state
  if (event?.type === 'job') return { phase: 'live', job: event.job, failures: 0 }
  if (event?.status === 404) return { ...state, phase: 'expired' }
  const failures = state.failures + 1
  if (failures >= JOB_POLL_FAILURE_LIMIT) return { ...state, phase: 'lost', failures }
  return { ...state, failures }
}

export function jobPollDone(state) {
  if (terminal(state)) return true
  const s = state.job?.state
  return state.phase === 'live' && (s === 'success' || s === 'failed')
}

export function jobReconnecting(state) {
  return !terminal(state) && state.failures > 0
}

export async function pollJob({ jobId, fetchJob, sleep, onState, signal, intervalMs = JOB_POLL_MS }) {
  let state = jobPollStart()
  onState(state)
  while (!signal.cancelled) {
    let event
    try {
      event = { type: 'job', job: await fetchJob(jobId) }
    } catch (err) {
      event = { type: 'error', status: err?.status ?? 0 }
    }
    if (signal.cancelled) return state
    state = jobPollStep(state, event)
    onState(state)
    if (jobPollDone(state)) return state
    await sleep(intervalMs)
  }
  return state
}
