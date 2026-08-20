const BASE = '/v1/miniapp'

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request(path, opts = {}) {
  const res = await fetch(BASE + path, {
    ...opts,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers ?? {}) },
  })
  if (!res.ok) {
    let code = 'unknown'
    try {
      const body = await res.json()
      // Backend error bodies are { code, message } (writeJSONError,
      // internal/backend/handler.go:57-60) -- the field is "code", not "error".
      code = body.code ?? code
    } catch {
      // ignore non-JSON error bodies
    }
    throw new ApiError(res.status, code, `${path} failed: ${res.status}`)
  }
  return res.json()
}

export function createSession(initData) {
  return request('/session', { method: 'POST', body: JSON.stringify({ init_data: initData }) })
}

export function fetchRouters() {
  return request('/routers')
}

export function fetchRouter(id) {
  return request(`/routers/${id}`)
}

// Пороги, по которым бот судит об этом роутере (miniappSettingsResp). Живут
// они в backend.yaml, и клиент их только читает.
export function fetchRouterSettings(id) {
  return request(`/routers/${id}/settings`)
}

export function fetchRouterChecks(id) {
  return request(`/routers/${id}/events`)
}

export function silenceIncident(routerID, check, ttl) {
  return request(`/routers/${routerID}/incidents/${encodeURIComponent(check)}/silence`, {
    method: 'POST',
    body: JSON.stringify({ ttl }),
  })
}

export function ackIncident(routerID, check) {
  return request(`/routers/${routerID}/incidents/${encodeURIComponent(check)}/ack`, { method: 'POST' })
}

export function muteIncident(routerID, check) {
  return request(`/routers/${routerID}/incidents/${encodeURIComponent(check)}/mute`, { method: 'POST' })
}

export function fetchIncidentHistory(routerID, check) {
  return request(`/routers/${routerID}/incidents/${encodeURIComponent(check)}/history`)
}

// Лента событий роутера (/timeline), а не история одной проверки
// (/incidents/{check}/history) -- см. комментарий у miniappTimelineResp.
export function fetchTimeline(routerID, days = 7) {
  return request(`/routers/${routerID}/timeline?days=${days}`)
}

export function fetchAccess(routerID) {
  return request(`/routers/${routerID}/access`)
}

export function addOperator(routerID, telegramUserID) {
  return request(`/routers/${routerID}/access/operators`, {
    method: 'POST',
    body: JSON.stringify({ telegram_user_id: telegramUserID }),
  })
}

export function removeOperator(routerID, telegramUserID) {
  return request(`/routers/${routerID}/access/operators/${telegramUserID}`, { method: 'DELETE' })
}

export function unbindOwner(routerID) {
  return request(`/routers/${routerID}/access/owner`, { method: 'DELETE' })
}

// Dispatches an allowlisted agent command for a router. Resolves to the raw
// body the backend sends on 202: { cmd_id }. That shape is wizardDeployResp
// (wizard_handler.go:537-539) -- miniappCommandHandler (miniapp_commands.go)
// reuses it verbatim via enqueueAgentCommandForUser (wizard_handler.go:1234),
// which encodes `wizardDeployResp{CmdID: id}`. The key is "cmd_id", NOT
// "command_id" -- callers must destructure { cmd_id } or they'll silently get
// undefined and poll `/commands/undefined` forever.
// Кабинеты провайдеров: что подключено и что можно выпустить. Ключей
// кабинета клиент не видит и не отправляет -- они живут у бота.
export function fetchVPNAccounts(routerID) {
  return request(`/routers/${routerID}/vpn`)
}

// Выпуск конфига: наружу уходит ТОЛЬКО выбор. Конфиг скачивает сервер и сам
// кладёт его в команду агенту; в ответ приезжает идентификатор команды.
export function issueVPNConfig(routerID, provider, optionID) {
  return request(`/routers/${routerID}/vpn/issue`, {
    method: 'POST',
    body: JSON.stringify({ provider, option_id: optionID }),
  })
}

// Мастер замены конфига: запуск и состояние. Состояние спрашивается ПРО
// РОУТЕР -- операцию могли запустить с другого устройства, и идентификатора
// задания у этого экрана может не быть вовсе.
export function fetchReplaceStatus(routerID) {
  return request(`/routers/${routerID}/replace`)
}

export function startReplace(routerID, body) {
  return request(`/routers/${routerID}/replace`, { method: 'POST', body: JSON.stringify(body) })
}

export function sendCommand(routerID, action, args = {}) {
  return request(`/routers/${routerID}/commands`, {
    method: 'POST',
    body: JSON.stringify({ action, args }),
  })
}

// Resolves to null when the agent hasn't answered yet: the backend returns 404
// result_not_ready by design (miniappCommandResultHandler, miniapp_commands.go),
// which is a "poll again", not a failure. Any other error still throws.
export function fetchCommandResult(routerID, cmdID, waitSec = 10) {
  return request(`/routers/${routerID}/commands/${encodeURIComponent(cmdID)}?wait_sec=${waitSec}`).catch((err) => {
    if (err instanceof ApiError && err.status === 404 && err.code === 'result_not_ready') return null
    throw err
  })
}
