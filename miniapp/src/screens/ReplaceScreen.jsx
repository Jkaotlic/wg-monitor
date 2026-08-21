import { useEffect, useState } from 'preact/hooks'
import { fetchVPNAccounts, fetchReplaceStatus, startReplace } from '../api.js'
import { accountSummary, optionRows } from '../cabinet.js'
import { replaceView, startErrorText, stepValue } from '../replace.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { DataRow } from '../ui/DataRow.jsx'

// Мастер замены конфига линии.
//
// Экран не владеет операцией: она идёт в бэкенде и переживает его закрытие.
// Поэтому при открытии он первым делом спрашивает, не идёт ли замена уже --
// её могли запустить с другого устройства, — и показывает шаги вместо формы.
//
// Прежний туннель остаётся на роутере всегда. Об этом сказано до нажатия, а
// не после: именно это свойство делает операцию обратимой.
const POLL_MS = 3000

export function ReplaceScreen({ routerID, tunnel, policyName, onClose, onDone }) {
  const [job, setJob] = useState(null)
  const [accounts, setAccounts] = useState(null)
  const [pick, setPick] = useState(null)
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)

  useEffect(() => {
    let alive = true
    const poll = () => {
      fetchReplaceStatus(routerID)
        .then((j) => {
          if (alive) setJob(j)
        })
        .catch(() => {})
    }
    poll()
    const timer = setInterval(poll, POLL_MS)
    fetchVPNAccounts(routerID)
      .then((data) => {
        if (alive) setAccounts(data.accounts ?? [])
      })
      .catch(() => {
        if (alive) setError('Кабинеты не ответили — выпустить конфиг не из чего.')
      })
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [routerID])

  const view = replaceView(job)

  function start() {
    if (!pick) return
    setStarting(true)
    setError('')
    startReplace(routerID, {
      provider: pick.provider,
      option_id: pick.option.id,
      old_tunnel_id: tunnel.id,
      policy_name: policyName,
    })
      .then((j) => setJob({ ...j, steps: j.steps ?? [] }))
      .catch((err) => setError(startErrorText(err)))
      .finally(() => setStarting(false))
  }

  const showForm = view.idle && !starting

  return (
    <Overlay title="Заменить конфиг" backLabel="Туннели" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">Заменить конфиг «{tunnel.name}»</h1>
        <p class="router-lastseen">
          Новый туннель встанет рядом, и только когда он заработает, политика «{policyName}»
          перейдёт на него. Прежний останется на роутере выключенным.
        </p>

        {error && <p class="state state-error">{error}</p>}

        {!view.idle && (
          <Section title={view.running ? 'Идёт замена' : 'Чем кончилось'}>
            <div class="card">
              {view.steps.map((s) => (
                <DataRow
                  key={s.name}
                  dot={s.tone === 'off' ? undefined : s.tone === 'sig' ? 'sig' : s.tone}
                  title={s.title}
                  code={s.detail}
                  value={stepValue(s.status, view.running)}
                  valueTone={s.tone === 'off' ? undefined : s.tone === 'sig' ? undefined : s.tone}
                />
              ))}
              {view.headline && (
                <p class={`card-foot${view.tone === 'danger' ? ' card-foot-bad' : ''}`}>{view.headline}</p>
              )}
              {view.rollback && <p class="card-foot">Откат: {view.rollback}</p>}
            </div>
            {view.running ? (
              <p class="hint">
                Можно закрыть приложение: замена идёт на сервере и договорит сама. Бот напишет в
                тему роутера, чем кончилось.
              </p>
            ) : (
              <button type="button" class="btn btn-primary btn-wide" onClick={() => { onDone?.(); onClose() }}>
                Понятно
              </button>
            )}
          </Section>
        )}

        {showForm && pick && (
          <Section title="Что произойдёт">
            <div class="card">
              <DataRow title="Заменяем" code={tunnel.id} value={tunnel.name} />
              <DataRow title="Новый конфиг" code={pick.provider} value={pick.option.label} />
              {/* В code стоит то, что оператор может сверить с роутером:
                  идентификатор туннеля, имя кабинета. Здесь раньше стояло
                  «route_policy_promote» -- имя внутренней команды бэкенда,
                  которого нет ни в одном экране роутера и которое человеку
                  сверять не с чем. У политики опознаётся она сама -- по имени. */}
              <DataRow title="Политика" value={policyName} />
              <p class="card-foot">
                Шесть шагов: выпустить конфиг, положить новым туннелем рядом, дождаться
                рукопожатия, перевести политику, проверить адрес выхода и только потом выключить
                прежний. Не сработает любой шаг — откат вернёт всё как было, а прежний туннель
                никуда не денется.
              </p>
            </div>
            <button type="button" class="btn btn-primary btn-wide" disabled={starting} onClick={start}>
              {starting ? 'Запускаем…' : 'Заменить конфиг'}
            </button>
            <button type="button" class="btn btn-ghost btn-wide" onClick={() => setPick(null)}>
              Выбрать другой
            </button>
          </Section>
        )}

        {showForm && !pick && (
          <>
            {accounts == null && !error && <p class="state">Спрашиваем кабинеты…</p>}
            {(accounts ?? []).map((acc) => {
              const summary = accountSummary(acc)
              const options = optionRows(acc)
              return (
                <Section key={acc.provider} title={summary.title}>
                  {summary.canIssue ? (
                    <ul class="card list-reset">
                      {options.map((o) => (
                        <ListRow
                          key={o.id}
                          title={o.label}
                          sub={o.note}
                          onClick={() => setPick({ provider: acc.provider, option: o })}
                        />
                      ))}
                    </ul>
                  ) : (
                    <div class="card">
                      <p class="traffic-detail">{summary.reason}</p>
                    </div>
                  )}
                </Section>
              )
            })}
          </>
        )}
      </div>
    </Overlay>
  )
}
