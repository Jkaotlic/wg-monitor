import { fetchCommandResult } from './api.js'
import { AGENT_OLDER_THAN_APP } from './labels.js'

// Ожидание итога команды, которую поставил не /commands, а свой маршрут
// сервера (удаление VPN-туннеля, загрузка .conf): ответ у них тот же {cmd_id},
// а useCommand умеет ставить команды только через /commands. Цикл тот же, что
// у useCommand: короткие хопы (релей KeenDNS рвёт всё, что дольше 15 секунд),
// последний хоп не перелетает дедлайн. Опрос -- тот же GET
// /routers/{id}/commands/{cmd_id}.
//
// alive() -- жив ли экран: закрытый экран не ждёт и чужой ответ не принимает.
// null -- не дождались (дедлайн или экран ушёл); ошибка сети -- исключение.
export async function waitCommand(routerID, cmdID, { deadlineMs = 90_000, waitSec = 10, alive = () => true, now = () => Date.now() } = {}) {
  const until = now() + deadlineMs
  while (alive() && now() < until) {
    const remainingSec = Math.max(1, Math.ceil((until - now()) / 1000))
    const res = await fetchCommandResult(routerID, cmdID, Math.min(waitSec, remainingSec))
    if (!alive()) return null
    if (res) return res
  }
  return null
}

// Спящий роутер отвечает минутами -- тот же дедлайн, что у экранов вкладок.
export function waitDeadlineMs(asleep) {
  return asleep ? 6 * 60_000 : 90_000
}

const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// Свои маршруты сервера ждут роутер не дольше нескольких секунд и отвечают
// «ещё спрашиваю» (удаление -- state:"checking", предпросмотр --
// state:"analyzing"). Клиент повторяет запрос с паузой; повтор не ставит
// роутеру вторую команду -- сервер переиспользует вопрос.
//
// {resp, settled}: resp -- последний ответ (null, если экран ушёл до первого),
// settled -- дождались ли ответа не «в процессе». Ошибка запроса -- исключение.
export async function repeatWhilePending(
  once,
  { pending, deadlineMs = 120_000, pauseMs = 1500, alive = () => true, now = () => Date.now(), sleep = pause } = {},
) {
  const until = now() + deadlineMs
  let resp = null
  while (alive()) {
    resp = await once()
    if (!pending(resp)) return { resp, settled: true }
    if (!alive() || now() >= until) break
    await sleep(pauseMs)
  }
  return { resp, settled: false }
}

// Итог команды словами. ok/fail/pending -- фразы экрана; сырой вывод агента
// идёт только хвостом после fail, а «unknown action» -- общими словами про
// версию агента.
export function commandOutcome(result, { ok, fail, pending }) {
  if (!result) return { tone: 'warn', text: pending, done: false }
  if (result.status === 'ok') return { tone: 'ok', text: ok, done: true }
  const output = String(result.output ?? '').trim()
  if (/^unknown action:/i.test(output)) return { tone: 'error', text: AGENT_OLDER_THAN_APP, done: false }
  return { tone: 'error', text: `${fail}: ${output || result.status}`, done: false }
}
