// Раскатка бэкенда из Парка: когда предлагать, что сказать на листе и как
// ждать перезапуска. Сервер после 202 скачивает новую версию и перезапускает
// сам себя -- приложение на это время без сервера, поэтому ждём по /healthz
// (он открыт без входа) и перезагружаем страницу, когда ответит новая версия.

export const DEPLOY_POLL_MS = 3000
export const DEPLOY_TIMEOUT_MS = 5 * 60_000
export const DEPLOY_RELOAD_MS = 2000

function bare(v) {
  return String(v ?? '').trim().replace(/^v/i, '')
}

// Сборка может отдавать версию и с «v», и без: сравниваем суть.
export function sameVersion(a, b) {
  const x = bare(a)
  return x !== '' && x === bare(b)
}

export function backendDeployOffer(fleet) {
  const backend = fleet?.backend ?? {}
  const target = String(backend.latest_version ?? '').trim()
  if (backend.update_available !== true || !target) return null
  return { target, label: `Обновить бэкенд до ${target}` }
}

export function backendDeploySheetText(target) {
  return {
    title: `Обновить бэкенд до ${target}?`,
    body: `Сервер скачает ${target} и перезапустится. Пока он перезапускается, приложение не отвечает — обычно минуту-две. Откатить бэкенд из приложения нельзя.`,
  }
}

const DEPLOY_ERRORS = {
  confirm_mismatch: 'Подтверждение не совпало',
  backend_update_not_configured: 'Раскатка бэкенда на этом сервере не настроена',
}

// Пустая строка -- «своей фразы нет»: лист скажет общее «Не получилось».
export function backendDeployErrorText(err) {
  return err?.serverMessage || DEPLOY_ERRORS[err?.code] || ''
}

export function deployWaitStart(target, at) {
  return { phase: 'waiting', target, startedAt: at, lastVersion: '' }
}

export function deployWaitStep(state, event) {
  if (state.phase === 'done' || state.phase === 'timeout') return state
  // Новая версия побеждает таймаут: ответ пришёл -- значит успели.
  if (event?.type === 'health' && sameVersion(event.version, state.target)) {
    return { ...state, phase: 'done', lastVersion: event.version }
  }
  if (event.at - state.startedAt >= DEPLOY_TIMEOUT_MS) return { ...state, phase: 'timeout' }
  if (event?.type === 'health') return { ...state, phase: 'waiting', lastVersion: String(event.version ?? '') }
  // Сетевые ошибки и 5xx во время перезапуска -- норма, а не провал.
  return { ...state, phase: 'restarting' }
}

export function deployWaitText(state) {
  const target = state.target
  switch (state.phase) {
    case 'done':
      return { title: `Готово, бэкенд ${target}`, line: 'Перезагружаем страницу…', tone: 'ok' }
    case 'timeout':
      return { title: 'Бэкенд не ответил новой версией за 5 минут', line: 'Проверьте сводку позже.', tone: 'bad' }
    case 'restarting':
      return { title: `Бэкенд обновляется до ${target}`, line: 'Сервер перезапускается…', tone: 'running' }
    default:
      return {
        title: `Бэкенд обновляется до ${target}`,
        line: state.lastVersion ? `Сервер ещё отвечает прежней версией ${state.lastVersion} — скачивает новую…` : 'Ждём ответа сервера…',
        tone: 'running',
      }
  }
}

export async function watchBackendDeploy({ target, fetchHealth, sleep, now, onState, signal, pollMs = DEPLOY_POLL_MS }) {
  let state = deployWaitStart(target, now())
  onState(state)
  while (!signal.cancelled) {
    let event
    try {
      const health = await fetchHealth()
      event = { type: 'health', version: health?.version ?? '', at: now() }
    } catch {
      event = { type: 'down', at: now() }
    }
    if (signal.cancelled) return state
    state = deployWaitStep(state, event)
    onState(state)
    if (state.phase === 'done' || state.phase === 'timeout') return state
    await sleep(pollMs)
  }
  return state
}
