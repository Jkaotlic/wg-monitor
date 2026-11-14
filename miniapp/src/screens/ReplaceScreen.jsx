import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { fetchVPNAccounts, fetchReplaceStatus, startReplace, fetchRouterSettings } from '../api.js'
import { cabinetPerms } from '../cabinetKeys.js'
import { accountSummary, optionRows } from '../cabinet.js'
import { replaceView, startErrorText, stepValue, replaceLeftover } from '../replace.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Q, Quoted } from '../ui/Q.jsx'

// Мастер замены конфига VPN-туннеля.
//
// Экран не владеет операцией: она идёт в бэкенде и переживает его закрытие.
// Поэтому при открытии он первым делом спрашивает, не идёт ли замена уже --
// её могли запустить с другого устройства, — и показывает шаги вместо формы.
//
// Прежний туннель остаётся на роутере всегда. Об этом сказано до нажатия, а
// не после: именно это свойство делает операцию обратимой.
const POLL_MS = 3000

// onOpenCabinet -- открыть «Откуда взять конфиг»: там админ, владелец и
// оператор отзывают выпущенную страну, когда в подписке нет мест.
// onOpenTunnel(tunnelID|null) -- к экрану VPN-туннеля, который мастер оставил на
// роутере (null -- к списку): разбираться с ним человек будет там.
// replaceCanRevoke -- кто может отозвать старый конфиг в мастере замены:
// админ, владелец и оператор (решение оператора 01.10).
// Правило роли -- одно, в cabinetPerms (cabinetKeys.js).
export function replaceCanRevoke(role) {
  return cabinetPerms(role).revoke
}

export function ReplaceScreen(props) {
  return <RouterReplaceScreen key={props.routerID} {...props} />
}

function RouterReplaceScreen({ routerID, tunnel, policyName, onClose, onDone, onOpenCabinet, onOpenTunnel }) {
  const [job, setJob] = useState(null)
  const [accounts, setAccounts] = useState(null)
  const [pick, setPick] = useState(null)
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  // Отзывать могут админ, владелец и оператор (replaceCanRevoke); роль не
  // узнали -- кнопки нет, слова те же.
  const [canRevoke, setCanRevoke] = useState(false)
  const alive = useRef(true)
  const startInFlight = useRef(false)
  const pollGeneration = useRef(0)

  useLayoutEffect(() => () => { alive.current = false }, [])

  useEffect(() => {
    let timer = null
    // Следующий опрос уходит только после ответа предыдущего: setInterval
    // наслаивал запросы друг на друга при медленной сети.
    const poll = () => {
      if (!alive.current) return
      // Пока сервер не подтвердил запуск, опрос может увидеть прежнее состояние.
      if (startInFlight.current) {
        timer = setTimeout(poll, POLL_MS)
        return
      }
      const generation = pollGeneration.current
      fetchReplaceStatus(routerID)
        .then((j) => {
          if (alive.current && generation === pollGeneration.current) setJob(j)
        })
        .catch(() => {})
        .finally(() => {
          if (alive.current) timer = setTimeout(poll, POLL_MS)
        })
    }
    poll()
    setCanRevoke(false)
    fetchRouterSettings(routerID)
      .then((st) => {
        if (alive.current) setCanRevoke(replaceCanRevoke(st?.role))
      })
      .catch(() => {})
    fetchVPNAccounts(routerID)
      .then((data) => {
        if (alive.current) setAccounts(data.accounts ?? [])
      })
      .catch(() => {
        if (alive.current) setError('Кабинеты не ответили — выпустить конфиг не из чего.')
      })
    return () => {
      if (timer) clearTimeout(timer)
    }
  }, [routerID])

  const view = replaceView(job)
  const leftover = onOpenTunnel ? replaceLeftover(view, tunnel) : null

  function start() {
    if (!pick || startInFlight.current) return
    startInFlight.current = true
    // Запрос, уже ушедший до запуска, тоже мог увидеть прежнее состояние.
    pollGeneration.current++
    setStarting(true)
    setError('')
    startReplace(routerID, {
      provider: pick.provider,
      option_id: pick.option.id,
      old_tunnel_id: tunnel.id,
      policy_name: policyName,
    })
      .then((j) => {
        if (alive.current) setJob({ ...j, steps: j.steps ?? [] })
      })
      .catch((err) => {
        if (alive.current) setError(startErrorText(err))
      })
      .finally(() => {
        startInFlight.current = false
        if (alive.current) setStarting(false)
      })
  }

  const showForm = view.idle && !starting

  return (
    <Overlay title="Заменить конфиг" backLabel="VPN-туннели" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">
          Заменить конфиг <Q>{tunnel.name}</Q>
        </h1>
        <p class="router-lastseen">
          Новый VPN-туннель встанет рядом, и только когда он заработает, правила <Q>{policyName}</Q>{' '}
          перейдут на него. Прежний останется на роутере выключенным.
        </p>

        {error && <p class="state state-error">{error}</p>}

        {!view.idle && (
          <Section title={view.running ? 'Идёт замена' : 'Чем кончилось'}>
            <div class="card card-rows">
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
            {leftover && (
              <div class="card replace-leftover">
                <p class="traffic-detail">
                  <Quoted text={leftover.text} />
                </p>
                <button
                  type="button"
                  class="btn btn-ghost btn-wide"
                  onClick={() => {
                    onDone?.()
                    onOpenTunnel(leftover.tunnelID)
                  }}
                >
                  <Quoted text={leftover.button} />
                </button>
              </div>
            )}
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
            <div class="card card-rows">
              <DataRow title="Заменяем" code={tunnel.id} value={tunnel.name} />
              <DataRow title="Новый конфиг" code={pick.provider} value={pick.option.label} />
              {/* В code стоит то, что оператор может сверить с роутером:
                  идентификатор туннеля, имя кабинета. Здесь раньше стояло
                  «route_policy_promote» -- имя внутренней команды бэкенда,
                  которого нет ни в одном экране роутера и которое человеку
                  сверять не с чем. У политики опознаётся она сама -- по имени. */}
              <DataRow title="Набор правил" value={policyName} />
              <p class="card-foot">
                Шесть шагов: выпустить настройки, поднять новый VPN-туннель рядом, дождаться
                обмена ключами, перевести правила, проверить адрес выхода и только потом выключить
                прежний. Не сработает любой шаг — откат вернёт всё как было, а прежний VPN-туннель
                никуда не денется.
              </p>
            </div>
            <div class="action-row">
              <button type="button" class="btn btn-primary" disabled={starting} onClick={start}>
                {starting ? 'Запускаем…' : 'Заменить конфиг'}
              </button>
              <button type="button" class="btn btn-ghost" onClick={() => setPick(null)}>
                Выбрать другой
              </button>
            </div>
          </Section>
        )}

        {showForm && !pick && (
          <>
            {accounts == null && !error && <p class="state">Спрашиваем кабинеты…</p>}
            {(accounts ?? []).map((acc) => {
              const summary = accountSummary(acc)
              const options = optionRows(acc).filter((o) => o.available)
              const openCabinet = summary.full && canRevoke && onOpenCabinet
              const fullBlock = summary.full && (
                <div class="card">
                  <p class="traffic-detail">
                    <Quoted text={openCabinet ? `${summary.fullNote} Освободить место можно в кабинете: отзовите одну из выпущенных стран.` : summary.reason} />
                  </p>
                  {openCabinet && (
                    <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenCabinet}>
                      Открыть кабинет
                    </button>
                  )}
                </div>
              )
              return (
                <Section key={acc.provider} title={summary.title}>
                  {fullBlock}
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
                    !summary.full && (
                      <div class="card">
                        <p class="traffic-detail">
                          <Quoted text={summary.reason} />
                        </p>
                      </div>
                    )
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
