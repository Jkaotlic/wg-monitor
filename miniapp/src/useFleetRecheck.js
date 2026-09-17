import { useState } from 'preact/hooks'
import { sendCommand, fetchCommandResult } from './api.js'

// Групповой опрос -- это N обычных force_recheck, а не новая власть над
// флотом: каждая команда идёт по своему роутеру через тот же allowlist и
// ту же проверку доступа. Поэтому здесь нет ни своего эндпоинта, ни своих
// прав -- только цикл и честный счёт ответивших. Общий для списка роутеров
// (телефон) и сводки парка (широкий экран).
//
// Хуки на роутер завести нельзя (их число менялось бы между рендерами),
// поэтому команды идут прямо через api и складываются в одно состояние.
export function useFleetRecheck(routers) {
  const [batch, setBatch] = useState(null)

  async function recheckAll() {
    const list = routers ?? []
    if (list.length === 0 || batch?.running) return
    setBatch({ total: list.length, ok: 0, failed: 0, done: 0, running: true })
    await Promise.all(
      list.map(async (r) => {
        let good = false
        try {
          const { cmd_id: id } = await sendCommand(r.id, 'force_recheck', {})
          const until = Date.now() + 90_000
          while (Date.now() < until) {
            const res = await fetchCommandResult(r.id, id, 10)
            if (res) {
              good = res.status === 'ok'
              break
            }
          }
        } catch {
          good = false
        }
        setBatch((prev) => ({
          ...prev,
          ok: prev.ok + (good ? 1 : 0),
          failed: prev.failed + (good ? 0 : 1),
          done: prev.done + 1,
          running: prev.done + 1 < prev.total,
        }))
      }),
    )
  }

  return { batch, recheckAll }
}
