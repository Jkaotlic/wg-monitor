import { useEffect, useRef, useState } from 'preact/hooks'
import { sendCommand, fetchCommandResult } from './api.js'

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

  // A sleeping-router deadline can run minutes long; if the screen is closed
  // (component unmounted) before it resolves, the in-flight poll must not
  // call setState on a dead component. This ref is the only thing every
  // async continuation below checks before touching state.
  const aliveRef = useRef(true)
  useEffect(
    () => () => {
      aliveRef.current = false
    },
    [],
  )

  async function run(action, args = {}, { deadlineMs = 90_000, waitSec = 25 } = {}) {
    if (!aliveRef.current) return null
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const { cmd_id: id } = await sendCommand(routerID, action, args)
      const until = Date.now() + deadlineMs
      while (Date.now() < until) {
        // Bounded hop: the backend caps wait_sec at 30 anyway
        // (miniappMaxCommandWaitSec, miniapp_commands.go), but capping the
        // LAST hop to whatever time remains keeps the loop from overshooting
        // the deadline by a near-full hop when the deadline is close.
        const remainingSec = Math.max(1, Math.ceil((until - Date.now()) / 1000))
        const res = await fetchCommandResult(routerID, id, Math.min(waitSec, remainingSec))
        if (!aliveRef.current) return null
        if (res) {
          setResult(res)
          return res
        }
      }
      if (aliveRef.current) {
        setError('Не дождались ответа за отведённое время. Если роутер спит, команда выполнится позже — откройте экран заново, чтобы увидеть результат.')
      }
    } catch (err) {
      if (aliveRef.current) setError(err.message)
    } finally {
      if (aliveRef.current) setBusy(false)
    }
    return null
  }

  return { busy, result, error, run }
}
