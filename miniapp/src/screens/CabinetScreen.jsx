import { useEffect, useState } from 'preact/hooks'
import { fetchVPNAccounts, issueVPNConfig, fetchCommandResult } from '../api.js'
import { accountSummary, optionRows } from '../cabinet.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { DataRow } from '../ui/DataRow.jsx'

// Кабинеты провайдеров: выпустить конфиг и сразу положить его на роутер.
//
// Ключей кабинета экран не спрашивает и не показывает: они живут у бота.
// Содержимое конфига через приложение тоже не проходит -- клиент отправляет
// только выбор, сервер сам скачивает конфиг и сам отдаёт его агенту. Поэтому
// здесь нет ни поля для файла, ни кнопки «скачать».
export function CabinetScreen({ routerID, asleep, onClose, onIssued }) {
  const [accounts, setAccounts] = useState(null)
  const [error, setError] = useState(null)
  const [pending, setPending] = useState(null)
  const [phase, setPhase] = useState('idle')
  const [outcome, setOutcome] = useState('')

  useEffect(() => {
    fetchVPNAccounts(routerID)
      .then((data) => setAccounts(data.accounts ?? []))
      .catch(() => setError('Не удалось прочитать кабинеты. Откройте экран заново.'))
  }, [routerID])

  async function issue() {
    if (!pending) return
    setPhase('running')
    setOutcome('')
    try {
      const { cmd_id: id, tunnel_name: name } = await issueVPNConfig(routerID, pending.provider, pending.option.id)
      const until = Date.now() + (asleep ? 6 * 60_000 : 90_000)
      while (Date.now() < until) {
        const res = await fetchCommandResult(routerID, id, 10)
        if (res) {
          setPhase('done')
          setOutcome(
            res.status === 'ok'
              ? `Конфиг выпущен и импортирован как «${name}». Он появится в списке туннелей.`
              : `Роутер не принял конфиг: ${res.output || res.status}`,
          )
          if (res.status === 'ok') onIssued?.()
          return
        }
      }
      setPhase('done')
      setOutcome('Конфиг выпущен, но роутер пока не подтвердил импорт. Откройте экран туннелей позже.')
    } catch (err) {
      setPhase('done')
      setOutcome(`Кабинет отказал: ${err.message}`)
    }
  }

  return (
    <Overlay title="Кабинеты провайдеров" backLabel={pending ? 'Назад' : 'Туннели'} onBack={pending ? () => { setPending(null); setPhase('idle') } : onClose}>
      <div class="screen">
        {error && <p class="state state-error">{error}</p>}
        {accounts == null && !error && <p class="state">Спрашиваем кабинеты…</p>}

        {pending ? (
          <Section title="Что произойдёт">
            <div class="card">
              <DataRow title="Кабинет" code={pending.provider} value={pending.title} />
              <DataRow title="Выпускаем" code={pending.option.id} value={pending.option.label} />
              <p class="card-foot">
                Конфиг скачает сервер и сразу отдаст его роутеру — через приложение он не
                проходит. На роутере появится новый туннель; прежние остаются на месте.
                {pending.option.note ? ' Этот конфиг уже выпускался: он будет перевыпущен, и старый перестанет работать.' : ''}
              </p>
            </div>
            {phase === 'running' && <p class="state">Кабинет выдаёт конфиг, роутер его принимает…</p>}
            {phase === 'done' && <p class="state">{outcome}</p>}
            {phase !== 'running' && (
              <button type="button" class="btn btn-primary btn-wide" onClick={issue}>
                {phase === 'done' ? 'Выпустить ещё раз' : 'Выпустить и положить на роутер'}
              </button>
            )}
          </Section>
        ) : (
          (accounts ?? []).map((acc) => {
            const summary = accountSummary(acc)
            const options = optionRows(acc)
            return (
              <Section key={acc.provider} title={summary.title}>
                <div class="card">
                  {summary.lines.map((line) => (
                    <p key={line} class="traffic-detail">{line}</p>
                  ))}
                  {/* Подвал отделяется линией от того, что над ним. Когда
                      над ним ничего нет, линия висит в пустоте -- тогда это
                      просто фраза в карточке. */}
                  {!summary.canIssue && (
                    <p class={summary.lines.length ? 'card-foot' : 'traffic-detail'}>{summary.reason}</p>
                  )}
                </div>
                {summary.canIssue && (
                  <ul class="card list-reset settings-card">
                    {options.map((o) => (
                      <ListRow
                        key={o.id}
                        title={o.label}
                        sub={o.note}
                        onClick={() => {
                          setPending({ provider: acc.provider, title: summary.title, option: o })
                          setPhase('idle')
                        }}
                      />
                    ))}
                  </ul>
                )}
              </Section>
            )
          })
        )}
      </div>
    </Overlay>
  )
}
