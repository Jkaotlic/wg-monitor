import { useEffect } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import {
  parseHrneoInventory,
  hrneoState,
  hrneoStatusLine,
  hrneoActions,
  hrneoSheet,
  hrneoRuleRows,
  inventoryNote,
  HRNEO_TEXTS,
} from '../hrneoBlock.js'
import { Section } from '../ui/Section.jsx'
import { Chip } from '../ui/Chip.jsx'
import { ListRow } from '../ui/ListRow.jsx'

const RULES_SHOWN = 50

// HydraRoute Neo в «Маршрутах»: установлен ли и запущен, запуск/остановка/
// перезапуск и его правила -- свёрнутым списком, только чтение. Состояние --
// из hrneo_inventory; не ответил -- из снимка маршрутизации экрана.
export function HrneoBlock({ routerID, asleep, snapshot, role, openSheet, onChanged }) {
  const inv = useCommand(routerID)
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const load = () => inv.run('hrneo_inventory', {}, deadline)

  useEffect(() => {
    load()
  }, [routerID])

  const inventory = inv.result?.status === 'ok' ? parseHrneoInventory(inv.result.output) : null
  const state = hrneoState({ inventory, snapshot })
  const line = hrneoStatusLine(state)
  const actions = typeof openSheet === 'function' ? hrneoActions(state, role) : []
  const rules = hrneoRuleRows(inventory, snapshot)
  const note = inventoryNote({ busy: inv.busy, result: inv.result, error: inv.error, inventory })

  // После любого исхода -- переспросить: и состояние, и снимок маршрутов
  // (правила по имени сайта могли перестать действовать).
  const ask = (name) =>
    openSheet(
      hrneoSheet({
        routerID,
        name,
        asleep,
        onResult: () => {
          load()
          onChanged?.()
        },
      }),
    )

  return (
    <Section title={HRNEO_TEXTS.title}>
      <div class="card">
        <p class="traffic-title hrneo-status">
          <Chip tone={line.tone}>{line.chip}</Chip>
        </p>
        <p class="traffic-detail">{line.text}</p>
        {note && <p class="card-foot">{note}</p>}
      </div>
      {actions.length > 0 && (
        <div class="command-actions hrneo-actions">
          {actions.map((a) => (
            <button key={a.name} type="button" class={`btn ${a.danger ? 'btn-danger' : 'btn-ghost'}`} onClick={() => ask(a.name)}>
              {a.label}
            </button>
          ))}
        </div>
      )}
      {rules.length > 0 && (
        <details class="checks-spoiler hrneo-rules">
          <summary class="section-title checks-spoiler-summary">{`${HRNEO_TEXTS.rules} · ${rules.length}`}</summary>
          <ul class="card list-reset">
            {rules.slice(0, RULES_SHOWN).map((r) => (
              <ListRow key={r.id} title={r.title} sub={r.sub} />
            ))}
          </ul>
          {rules.length > RULES_SHOWN && <p class="admin-note">{`Показаны первые ${RULES_SHOWN} из ${rules.length}.`}</p>}
          <p class="admin-note">{HRNEO_TEXTS.readOnly}</p>
        </details>
      )}
    </Section>
  )
}
