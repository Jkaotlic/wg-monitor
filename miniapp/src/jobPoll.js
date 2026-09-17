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

// denied -- сервер отказал не сетью: 401/403/503 и прочие 4xx, кроме 404.
// Повторять бессмысленно, экран говорит словами сервера.
function terminal(state) {
  return state.phase === 'expired' || state.phase === 'lost' || state.phase === 'denied'
}

function deniedStatus(status) {
  return status === 503 || (status >= 400 && status < 500 && status !== 404)
}

export function jobPollStep(state, event) {
  if (terminal(state)) return state
  if (event?.type === 'job') return { phase: 'live', job: event.job, failures: 0 }
  if (event?.status === 404) return { ...state, phase: 'expired' }
  if (deniedStatus(event?.status)) return { ...state, phase: 'denied', message: String(event?.message ?? '') }
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
      event = { type: 'error', status: err?.status ?? 0, message: err?.serverMessage ?? '' }
    }
    if (signal.cancelled) return state
    state = jobPollStep(state, event)
    onState(state)
    if (jobPollDone(state)) return state
    await sleep(intervalMs)
  }
  return state
}
