const BASE = '/v1/miniapp'
const DASHBOARD = '/v1/dashboard'

export class ApiError extends Error {
  // serverMessage -- фраза, которую прислал сервер. Отдельным полем, а не
  // вместо message: старые ответы бэкенда по-английски, и подставлять их
  // человеку на экран нельзя. Новые поверхности говорят по-русски и сами
  // решают, показать ли эту фразу вместо своей.
  constructor(status, code, message, serverMessage = '') {
    super(message)
    this.status = status
    this.code = code
    this.serverMessage = serverMessage
  }
}

// 401 посреди работы в веб-управлении значит «кука дашборда истекла»:
// оболочка переводит человека на экран входа, сохраняя место в адресе.
// Обработчик ставит только оболочка web; в Telegram его нет, и поведение
// прежнее. /session сюда не входит: там 401 -- обычный ответ «не вошёл».
let onUnauthorized = null

export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn
  return () => {
    if (onUnauthorized === fn) onUnauthorized = null
  }
}

async function request(path, opts = {}, base = BASE) {
  // Content-Type уходит всегда, и у DELETE без тела тоже: в веб-управлении
  // бэкенд отвергает не-GET без JSON-заголовка (защита от межсайтовой формы).
  const res = await fetch(base + path, {
    ...opts,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers ?? {}) },
  })
  if (!res.ok) {
    let code = 'unknown'
    let serverMessage = ''
    try {
      const body = await res.json()
      // Backend error bodies are { code, message } (writeJSONError,
      // internal/backend/handler.go:57-60) -- the field is "code", not "error".
      code = body.code ?? code
      serverMessage = typeof body.message === 'string' ? body.message : ''
    } catch {
      // ignore non-JSON error bodies
    }
    if (res.status === 401 && base === BASE && path !== '/session' && onUnauthorized) onUnauthorized()
    throw new ApiError(res.status, code, `${path} failed: ${res.status}`, serverMessage)
  }
  if (res.status === 204) return null
  return res.json()
}

export function createSession(initData) {
  return request('/session', { method: 'POST', body: JSON.stringify({ init_data: initData }) })
}

// Кто я -- по куке мини-аппа или куке веб-управления. В браузере это
// единственный способ узнать, вошёл ли человек: initData там нет.
export function fetchSession() {
  return request('/session')
}

export function dashboardLogin(token) {
  return request('/login', { method: 'POST', body: JSON.stringify({ token }) }, DASHBOARD)
}

// Обмен личной ссылки из мини-аппа на куку веб-управления.
export function redeemWebLink(token) {
  return request('/web-link/redeem', { method: 'POST', body: JSON.stringify({ token }) }, DASHBOARD)
}

export function dashboardLogout() {
  return request('/logout', { method: 'POST' }, DASHBOARD)
}

export function fetchRouters() {
  return request('/routers')
}

export function fetchRouter(id) {
  return request(`/routers/${id}`)
}

// Сводка всего парка -- только для админа: сервер отвечает 404 всем
// остальным, и экран этот вопрос не переспрашивает.
export function fetchFleet() {
  return request('/fleet')
}

// Одноразовый билет на панель роутера: адрес панели в ответ не входит, только
// путь билета, который открывается во внешнем браузере.
export function createPanelTicket(id) {
  return request(`/routers/${id}/panel/ticket`, { method: 'POST' })
}

// Личная ссылка на веб-управление. Ответ несёт и саму ссылку, и слова про
// срок с лимитом: своих текстов про «12 часов» клиент не сочиняет.
export function createWebLink() {
  return request('/web-link', { method: 'POST' })
}

// Пороги, по которым бот судит об этом роутере (miniappSettingsResp). Живут
// они в backend.yaml, и клиент их только читает.
export function fetchRouterSettings(id) {
  return request(`/routers/${id}/settings`)
}

// Личный выключатель уведомлений об этом роутере. Личный -- потому что у
// роутера несколько получателей, и решение каждого касается только его.
export function setRouterNotify(id, muted) {
  return request(`/routers/${id}/notify`, {
    method: 'PUT',
    body: JSON.stringify({ muted }),
  })
}

// Обновление агента на роутере -- действие бэкенда, а не команда из белого
// списка: сервер сам сверяет набранное имя, запрещает откат и ставит
// отметку, которая доживает до включения выключенного роутера. Цель версии
// по умолчанию считает сервер (его собственная версия), поэтому поле уходит
// только когда экран его явно выбрал.
// allowDowngrade -- человек разрешил откат на листе «Другая версия»; без
// него сервер откажет downgrade_rejected. Поле уходит только со значением true.
export function updateRouterAgent(routerID, confirm, targetVersion = '', allowDowngrade = false) {
  const body = targetVersion ? { confirm, target_version: targetVersion } : { confirm }
  if (targetVersion && allowDowngrade) body.allow_downgrade = true
  return request(`/routers/${routerID}/agent/update`, { method: 'POST', body: JSON.stringify(body) })
}

// Снять отложенное обновление. cleared=false -- снимать было нечего (уже
// поставилось или сняли раньше), и это не ошибка.
export function cancelRouterAgentUpdate(routerID) {
  return request(`/routers/${routerID}/agent/update/cancel`, { method: 'POST' })
}

// Обновить всех отставших. Итог -- по роутеру: поставлено, ждёт включения,
// пропущено с причиной или ошибка. Слово подтверждения сервер сверяет сам.
export function updateFleetAgents(confirm) {
  return request('/fleet/agent/update', { method: 'POST', body: JSON.stringify({ confirm }) })
}

// Оживление агента: пароль уходит один раз, в теле этого POST, и больше
// клиентом не читается -- сервер его не возвращает никаким маршрутом. Тело
// собирает revive.js (пустых полей в нём нет).
export function reviveRouterAgent(routerID, body) {
  return request(`/routers/${routerID}/agent/revive`, { method: 'POST', body: JSON.stringify(body) })
}

// Отмена оживления: сервер перестаёт ждать роутер и стирает пароль.
// cleared=false -- снимать было нечего, это не ошибка.
export function cancelRouterAgentRevive(routerID) {
  return request(`/routers/${routerID}/agent/revive`, { method: 'DELETE' })
}

// Переустановка агента сейчас и перенаправление на другой бэкенд. Пароли
// уходят один раз, в теле этого POST; ответ -- {job_id} для «Хода работы».
// Тело собирает agentJobs.js.
export function reinstallRouterAgent(routerID, body) {
  return request(`/routers/${routerID}/agent/reinstall`, { method: 'POST', body: JSON.stringify(body) })
}

export function repointRouterAgent(routerID, body) {
  return request(`/routers/${routerID}/agent/repoint`, { method: 'POST', body: JSON.stringify(body) })
}

export function fetchRouterChecks(id) {
  return request(`/routers/${id}/events`)
}

// Снимок версий, вышедшие обновления и -- отдельно -- причины незнания.
// Экран больше не зависит от нажатия: версии живут в базе и переживают
// рестарт бэкенда.
export function fetchRouterVersions(id) {
  return request(`/routers/${id}/versions`)
}

// «Отложить на неделю» или «Скрыть эту новость». Решение касается всего
// роутера (строка новости живёт на роутере, а не у человека), поэтому сервер
// пустит сюда только владельца и админа. Версию, о которой речь, считает сам
// сервер -- присланная клиентом могла бы спрятать выпуск, которого человек
// не видел.
export function setUpdateReminder(id, component, action, until) {
  return request(`/routers/${id}/updates/${encodeURIComponent(component)}`, {
    method: 'PUT',
    body: JSON.stringify(until ? { action, until } : { action }),
  })
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
// raw=1 просит сырую ленту событий вместо свёрнутых происшествий: она нужна
// тому, кто полез разбираться, и у неё свой потолок в пятьсот строк.
export function fetchTimeline(routerID, days = 7, { raw = false } = {}) {
  const suffix = raw ? '&raw=1' : ''
  return request(`/routers/${routerID}/timeline?days=${days}${suffix}`)
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

// Назначить владельца роутеру без владельца. owner -- { telegram_user_id }
// или { me: true }: тогда номер бэкенд берёт из сессии нажавшего, а не из
// тела запроса. Занятого владельца бэкенд не заменит (owner_exists).
export function setOwner(routerID, owner) {
  return request(`/routers/${routerID}/access/owner`, {
    method: 'PUT',
    body: JSON.stringify(owner),
  })
}

// Dispatches an allowlisted agent command for a router. Resolves to the raw
// body the backend sends on 202: { cmd_id }. That shape is wizardDeployResp
// (wizard_handler.go:537-539) -- miniappCommandHandler (miniapp_commands.go)
// reuses it verbatim via enqueueAgentCommandForUser (wizard_handler.go:1234),
// which encodes `wizardDeployResp{CmdID: id}`. The key is "cmd_id", NOT
// "command_id" -- callers must destructure { cmd_id } or they'll silently get
// undefined and poll `/commands/undefined` forever.
// Кабинеты провайдеров: подписка и что можно выпустить. Ключей и кодов
// кабинета этот ответ не несёт -- они вводятся на экране кабинета
// (addCabinetSecret) и наружу не отдаются никогда, даже маской здесь.
export function fetchVPNAccounts(routerID) {
  return request(`/routers/${routerID}/vpn`)
}

// Выпуск конфига: наружу уходит ТОЛЬКО выбор. Конфиг скачивает сервер и сам
// кладёт его в команду агенту; в ответ приезжает идентификатор команды.
// instanceID -- свой сервер (только админ); у кабинетов провайдеров его нет,
// и ключ в теле не появляется вовсе.
export function issueVPNConfig(routerID, provider, optionID, instanceID = '') {
  const body = { provider, option_id: optionID }
  if (instanceID) body.instance_id = instanceID
  return request(`/routers/${routerID}/vpn/issue`, {
    method: 'POST',
    body: JSON.stringify(body),
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

// Починка VPN-туннеля: запуск, состояние и выключатель полуавтомата. Состояние,
// как и у замены, спрашивается ПРО РОУТЕР: починку мог запустить сторож сам,
// и идентификатора задания у экрана не будет вовсе.
export function fetchRepairStatus(routerID) {
  return request(`/routers/${routerID}/repair`)
}

export function startRepair(routerID, checkName) {
  return request(`/routers/${routerID}/repair`, {
    method: 'POST',
    body: JSON.stringify({ check_name: checkName }),
  })
}

export function setAutoRepair(routerID, enabled) {
  return request(`/routers/${routerID}/repair/auto`, {
    method: 'PUT',
    body: JSON.stringify({ enabled }),
  })
}

// confirm -- набранное человеком имя роутера. Сервер сверяет его сам для
// необратимых действий (прошивка, перезагрузка) и отвечает confirm_mismatch;
// пустое поле не отправляется вовсе.
export function sendCommand(routerID, action, args = {}, confirm = '') {
  const body = confirm ? { action, args, confirm } : { action, args }
  return request(`/routers/${routerID}/commands`, {
    method: 'POST',
    body: JSON.stringify(body),
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

// Раскатка бэкенда до версии. confirm -- набранная человеком версия: сервер
// сверяет её сам, проверка на листе -- только пауза. Отката отсюда нет.
export function deployBackend(targetVersion, confirm) {
  return request('/backend/deploy', {
    method: 'POST',
    body: JSON.stringify({ target_version: targetVersion, confirm }),
  })
}

// /healthz живёт вне /v1/miniapp и отвечает без входа: во время раскатки
// бэкенд перезапускается, и спрашивать его «кто ты» можно только здесь.
// Кэш запрещён: иначе браузер покажет прежнюю версию после перезапуска.
export function fetchHealth() {
  return request('/healthz', { cache: 'no-store' }, '')
}

// Добавление роутера: kind "provision" -- установка сейчас (202 {job_id}),
// "register" -- только токен (201 {raw_token, …}). Тело собирает
// provisionWizard.js; пароли уходят один раз, этим запросом.
export function startProvision(body) {
  return request('/provision', { method: 'POST', body: JSON.stringify(body) })
}

// Ход задания установки/переустановки/перенаправления. 404 -- задание
// истекло (сервер хранит итог 30 минут).
export function fetchJob(jobID) {
  return request(`/jobs/${encodeURIComponent(jobID)}`)
}

// Как сервер добирается до роутера: панель awg-manager, SSH, раскатка, MAC.
// Только админ; /fleet эти поля намеренно не отдаёт.
export function fetchAgentConnection(routerID) {
  return request(`/routers/${routerID}/agent/connection`)
}

export function saveAgentConnection(routerID, body) {
  return request(`/routers/${routerID}/agent/connection`, { method: 'PUT', body: JSON.stringify(body) })
}

// Кабинеты роутера (цикл 3 «бот без слеш-команд»): ключи Amnezia Premium и
// коды HideMy.name вводятся в приложении. Секрет уходит только телом POST
// по HTTPS и не возвращается никогда -- в ответах маска. В адрес запроса,
// журнал и консоль он не попадает.
const CABINET_SECRET_PATH = { amnezia: 'keys', hidemy: 'codes' }
const CABINET_SECRET_FIELD = { amnezia: 'vpn_key', hidemy: 'access_code' }

function cabinetKind(kind) {
  if (!Object.hasOwn(CABINET_SECRET_PATH, kind)) throw new Error(`unknown cabinet: ${kind}`)
  return kind
}

export function fetchCabinets(routerID) {
  return request(`/routers/${routerID}/cabinets`)
}

export function addCabinetSecret(routerID, kind, secret, label = '') {
  const k = cabinetKind(kind)
  return request(`/routers/${routerID}/cabinets/${k}/${CABINET_SECRET_PATH[k]}`, {
    method: 'POST',
    body: JSON.stringify({ [CABINET_SECRET_FIELD[k]]: secret, label }),
  })
}

export function setCabinetActive(routerID, kind, id) {
  const k = cabinetKind(kind)
  return request(`/routers/${routerID}/cabinets/${k}/active`, { method: 'PUT', body: JSON.stringify({ id }) })
}

export function deleteCabinetSecret(routerID, kind, id) {
  const k = cabinetKind(kind)
  return request(`/routers/${routerID}/cabinets/${k}/${CABINET_SECRET_PATH[k]}/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// Отзыв выпущенной страны освобождает место в подписке. confirm -- набранное
// имя роутера: сервер сверяет его сам.
export function revokeAmneziaSlot(routerID, country, confirm) {
  return request(`/routers/${routerID}/cabinets/amnezia/revoke`, {
    method: 'POST',
    body: JSON.stringify({ country, confirm }),
  })
}

// .conf в личку нажавшему: файл отправляет бот, через приложение содержимое
// конфига не проходит (контракт miniapp_vpn.go). Выбор -- option_id, как у
// выпуска (сверка с частью 1).
export function sendVPNConf(routerID, { provider, option = '', instanceID = '' }) {
  const body = { provider, option_id: option }
  if (instanceID) body.instance_id = instanceID
  return request(`/routers/${routerID}/vpn/send-conf`, { method: 'POST', body: JSON.stringify(body) })
}

// Свои VPN-серверы -- только админ. SSH-пароль уходит только телом POST/PUT
// и только когда введён: пустое поле значит «не менять», и ключа в теле нет.
export function fetchSelfhosted() {
  return request('/selfhosted')
}

export function createSelfhosted(body) {
  return request('/selfhosted', { method: 'POST', body: JSON.stringify(body) })
}

export function updateSelfhosted(id, body) {
  return request(`/selfhosted/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) })
}

export function toggleSelfhosted(id, enabled) {
  return request(`/selfhosted/${encodeURIComponent(id)}/toggle`, { method: 'POST', body: JSON.stringify({ enabled }) })
}

export function deleteSelfhosted(id, confirm) {
  return request(`/selfhosted/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ confirm }) })
}

export function checkSelfhosted(id) {
  return request(`/selfhosted/${encodeURIComponent(id)}/check`, { method: 'POST' })
}
