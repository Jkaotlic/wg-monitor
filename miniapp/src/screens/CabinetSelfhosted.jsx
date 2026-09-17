import { selfhostedIssueRows, SELFHOSTED_TEXTS } from '../selfhostedForm.js'
import { CABINET_TEXTS } from '../cabinetKeys.js'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'

// Вкладка «Свой сервер» (только админ): выбрать включённый сервер, дальше --
// тот же экран выпуска, что у кабинетов. Выключенные не показываются:
// выпуск с них сервер всё равно отвергнет.
export function CabinetSelfhosted({ instances, error, onPick }) {
  if (error) return <p class="state state-error">{error}</p>
  if (instances == null) return <p class="state">{CABINET_TEXTS.instancesLoading}</p>
  const rows = selfhostedIssueRows(instances)
  return (
    <Section title="Свой сервер">
      <p class="hint">{SELFHOSTED_TEXTS.issueIntro}</p>
      {rows.length === 0 ? (
        <p class="state">{SELFHOSTED_TEXTS.noEnabled}</p>
      ) : (
        <ul class="card list-reset settings-card">
          {rows.map((r) => (
            <ListRow
              key={r.id}
              title={r.title}
              sub={r.sub}
              onClick={() => onPick({ provider: 'selfhosted', title: 'Свой сервер', option: { id: r.id, label: r.title, note: '' }, instanceID: r.id })}
            />
          ))}
        </ul>
      )}
    </Section>
  )
}
