import { useState } from 'preact/hooks'
import { Overlay } from '../ui/Overlay.jsx'
import { Chip } from '../ui/Chip.jsx'
import { sortByUrgency, fleetRow, batchProgress } from '../fleet.js'
import { sendCommand, fetchCommandResult } from '../api.js'

// Список роутеров: сломанное сверху, пять точек вместо цифр. Открывается
// только у того, кому доступен не один роутер -- владельцу одного показывать
// список незачем.
//
// Точки -- те же пять служб, что нарисованы лампами на экране роутера, и
// значение у них то же самое. Серая значит «роутер не сказал»: службу, о
// которой не было отчёта, нельзя красить ни зелёным, ни красным.
export function FleetOverlay({ routers, currentID, onPick, onClose }) {
  const rows = sortByUrgency(routers).map(fleetRow)
  const broken = rows.filter((r) => r.pill.tone === 'danger').length
  const [batch, setBatch] = useState(null)

  // Групповой опрос -- это N обычных force_recheck, а не новая власть над
  // флотом: каждая команда идёт по своему роутеру через тот же allowlist и
  // ту же проверку доступа. Поэтому здесь нет ни своего эндпоинта, ни своих
  // прав -- только цикл и честный счёт ответивших.
  //
  // Хуки на роутер завести нельзя (их число менялось бы между рендерами),
  // поэтому команды идут прямо через api и складываются в одно состояние.
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

  return (
    <Overlay title="Мои роутеры" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">Мои роутеры</h1>
        <p class="router-lastseen">
          {broken === 0
            ? `Все ${rows.length} в порядке.`
            : `Сломанное сверху: ${broken} из ${rows.length} требуют внимания.`}
        </p>

        <div class="card">
          {rows.map((r) => (
            <button
              key={r.id}
              type="button"
              class={`fleet-row${r.id === currentID ? ' fleet-row-current' : ''}`}
              onClick={() => onPick(r.id)}
            >
              <span class="fleet-main">
                <span class="fleet-name">
                  <span class="row-title">{r.nickname}</span>
                  <Chip tone={r.pill.tone}>{r.pill.text}</Chip>
                </span>
                <span class="fleet-sub">{r.sub}</span>
                <span class="fleet-dots">
                  {r.dots.map((d) => (
                    <i key={d.key} class={`fleet-dot fleet-dot-${d.tone}`} />
                  ))}
                </span>
              </span>
              <svg class="list-row-chevron" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M 6 3 L 11 8 L 6 13" />
              </svg>
            </button>
          ))}
          <p class="card-foot">
            <b>Пять точек — пять служб роутера:</b> адреса сайтов, интернет, обход блокировок,
            панель роутера, связь с ботом. Серая значит «роутер не сказал», а не «сломано».
          </p>
        </div>

        {rows.length > 1 && (
          <>
            <button type="button" class="btn btn-ghost btn-wide" disabled={batch?.running} onClick={recheckAll}>
              {batch?.running ? 'Опрашиваем…' : 'Опросить все'}
            </button>
            <p class="hint">
              {batchProgress(batch) ||
                'Каждый роутер переспросит себя сам. Ничего не меняет; спящие ответят, когда проснутся.'}
            </p>
          </>
        )}
      </div>
    </Overlay>
  )
}
