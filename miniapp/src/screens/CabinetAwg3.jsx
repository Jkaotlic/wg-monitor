import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'

// Панели, с которых можно выпустить конфиг на этот роутер. Список уже
// отобран сервером (miniappCanIssueAwg3) -- экран его не фильтрует.
export function CabinetAwg3({ panels, error, onPick }) {
  if (error) return <p class="state state-error">{error}</p>
  if (panels == null) return <p class="state">Загружаем панели…</p>
  return (
    <>
      {panels.map((p) => (
        <Section key={p.id} title={`Панель «${p.label || p.id}»`}>
          {p.unavailable ? (
            <p class="state">Панель сейчас не отвечает — попробуйте позже.</p>
          ) : p.ifaces.length === 0 ? (
            <p class="state">На панели нет интерфейсов.</p>
          ) : (
            <ul class="card list-reset settings-card">
              {p.ifaces.map((i) => (
                <ListRow
                  key={i.id}
                  title={i.title || i.id}
                  sub={i.id}
                  onClick={() => onPick({ provider: 'awg3panel', title: `Панель «${p.label || p.id}»`, instanceID: p.id, option: { id: i.id, label: i.title || i.id, note: '' } })}
                />
              ))}
            </ul>
          )}
        </Section>
      ))}
    </>
  )
}
