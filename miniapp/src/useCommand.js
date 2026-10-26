import { useEffect, useRef, useState } from 'preact/hooks'
import { sendCommand, fetchCommandResult, isTransient, COMMAND_GONE_TEXT } from './api.js'
import { asleepNote, SENT_NO_RESULT_TEXT, COMMAND_DEADLINE_TEXT } from './maintenance.js'
import { errorText, SESSION_TEXT } from './errorText.js'
import { JOB_POLL_MS, retryPauseMs } from './jobPoll.js'

// Commands are asynchronous by nature: the backend queues them, the agent
// picks them up on its own long-poll, and only then is there a result. A
// sleeping mobile router may take minutes. So this hook owns the whole wait,
// and callers get plain state (busy/result/error) instead of a promise that
// lies about being immediate.
//
// sendCommand's 202 body is wizardDeployResp verbatim (wizard_handler.go:
// 537-539) -- the id field is `cmd_id`, NOT `command_id`. Destructuring the
// wrong key silently yields `undefined` and this hook would poll
// `/routers/<id>/commands/undefined` forever without ever erroring, so the
// bug would be invisible until someone watched the network tab.
//
// deadlineMs/waitSec are options on `run`, not hook state: the caller (a
// CommandButton-style component) is the one that knows whether the router is
// already believed asleep and can widen the deadline for that call only,
// without a second hook instance.
export function useCommand(routerID) {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState(null)
  const [error, setError] = useState(null)
  // Код отказа сервера (ApiError.code, например invalid_domain) рядом с
  // текстом: текст у api.js -- «<путь> failed: 400», и по нему экран не
  // отличит «ваш ввод не годится» от «роутер не ответил». error остаётся
  // строкой -- на неё опираются все экраны.
  const [errorCode, setErrorCode] = useState(null)
  // sleepNote -- «роутер спит, команда подождёт N минут»: сервер говорит это
  // в ответе на постановку, и экран обязан пересказать, а не обещать ответ.
  const [sleepNote, setSleepNote] = useState('')
  // sent -- команда поставлена (получен cmd_id). После этого сбой опроса уже
  // не «команда не отправлена»: роутер её мог получить (MINI-01).
  const [sent, setSent] = useState(false)

  // A sleeping-router deadline can run minutes long; if the screen is closed
  // (component unmounted) before it resolves, the in-flight poll must not
  // call setState on a dead component. This ref is the only thing every
  // async continuation below checks before touching state.
  const aliveRef = useRef(true)
  const sentRef = useRef(false)
  useEffect(
    () => () => {
      aliveRef.current = false
    },
    [],
  )

  async function run(action, args = {}, { deadlineMs = 90_000, waitSec = 10, confirm = '' } = {}) {
    if (!aliveRef.current) return null
    setBusy(true)
    setError(null)
    setErrorCode(null)
    setResult(null)
    setSleepNote('')
    setSent(false)
    sentRef.current = false
    try {
      const posted = await sendCommand(routerID, action, args, confirm)
      const id = posted.cmd_id
      sentRef.current = true
      if (aliveRef.current) {
        setSent(true)
        setSleepNote(asleepNote(posted))
      }
      const until = Date.now() + deadlineMs
      let lostPoll = false
      while (Date.now() < until) {
        // Bounded hop: the backend caps wait_sec at 12 anyway
        // (miniappMaxCommandWaitSec, miniapp_commands.go) -- релей KeenDNS
        // рвёт всё, что дольше 15 секунд, и обрыв здесь читался бы как
        // ошибка, а не «ещё не готово». Capping the
        // LAST hop to whatever time remains keeps the loop from overshooting
        // the deadline by a near-full hop when the deadline is close.
        const remainingSec = Math.max(1, Math.ceil((until - Date.now()) / 1000))
        let res
        try {
          res = await fetchCommandResult(routerID, id, Math.min(waitSec, remainingSec))
        } catch (err) {
          // Временный сбой релея на опросе: команда уже у роутера, ждём
          // дальше до дедлайна, с паузой, чтобы не молотить мёртвый релей.
          if (!isTransient(err)) throw err
          lostPoll = true
          if (!aliveRef.current) return null
          await new Promise((resolve) => setTimeout(resolve, retryPauseMs(err, JOB_POLL_MS, until - Date.now())))
          continue
        }
        if (!aliveRef.current) return null
        // Удачный опрос снимает отметку сбоя: к тексту «ушла, итога нет»
        // ведёт только сбой ПОСЛЕДНЕГО опроса, а не любой давний.
        lostPoll = false
        if (res) {
          // Для какого роутера спрашивали: экран мог переключиться, пока ответ шёл.
          const tagged = { ...res, for_router: routerID }
          setResult(tagged)
          return tagged
        }
      }
      if (aliveRef.current) {
        setError(lostPoll ? SENT_NO_RESULT_TEXT : COMMAND_DEADLINE_TEXT)
      }
    } catch (err) {
      if (aliveRef.current) {
        // Фраза для человека, а не «/routers/7/commands failed: 400» (v0.50).
        const text = errorText(err)
        // После cmd_id не-временный сбой опроса (например 500) -- тоже «ушла,
        // итога нет»; своими остаются только «сессия истекла» и «команда исчезла».
        setError(sentRef.current && text !== SESSION_TEXT && text !== COMMAND_GONE_TEXT ? SENT_NO_RESULT_TEXT : text)
        setErrorCode(sentRef.current ? null : (err?.code ?? null))
      }
    } finally {
      if (aliveRef.current) setBusy(false)
    }
    return null
  }

  return { busy, result, error, errorCode, sleepNote, sent, run }
}
