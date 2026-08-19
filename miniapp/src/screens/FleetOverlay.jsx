import { Overlay } from '../ui/Overlay.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { sortByUrgency } from '../fleet.js'
import { humanAge, statusLabel } from '../labels.js'

// Список роутеров: сломанное сверху. Открывается только у того, кому доступен
// не один роутер -- владельцу одного показывать список незачем.
export function FleetOverlay({ routers, currentID, onPick, onClose }) {
  const rows = sortByUrgency(routers)
  return (
    <Overlay title="Мои роутеры" onBack={onClose}>
      <div class="screen">
        <p class="muted">Выберите роутер — откроется его экран.</p>
        <ul class="card list-reset">
          {rows.map((r) => (
            <ListRow
              key={r.id}
              title={r.nickname}
              sub={
                r.last_seen_age_sec != null
                  ? `последний ответ ${humanAge(r.last_seen_age_sec)} назад`
                  : 'ещё ни разу не выходил на связь'
              }
              right={<span class={`badge badge-${r.status}`}>{statusLabel(r.status)}</span>}
              tone={r.id === currentID ? 'current' : undefined}
              onClick={() => onPick(r.id)}
            />
          ))}
        </ul>
      </div>
    </Overlay>
  )
}
