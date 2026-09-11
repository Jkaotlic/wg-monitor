import { useEffect, useState } from 'preact/hooks'
import { fetchRepairStatus, startRepair } from '../api.js'
import { repairView } from '../repair.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Quoted } from '../ui/Q.jsx'

// Экран починки VPN-туннеля.
//
// Операцией владеет бэкенд, а не экран: её мог запустить сторож сам, пока
// приложение было закрыто. Поэтому экран сначала спрашивает состояние и
// только потом решает, показывать кнопку или ход работ.
const POLL_MS = 2000

function StepMark({ state, index }) {
  if (state === 'done') {
    return (
      <span class="repair-mark repair-mark-done" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
          <path d="m20 6-11 11-5-5" />
        </svg>
      </span>
    )
  }
  if (state === 'failed') {
    return (
      <span class="repair-mark repair-mark-failed" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round">
          <path d="M18 6 6 18M6 6l12 12" />
        </svg>
      </span>
    )
  }
  if (state === 'active') {
    return <span class="repair-mark repair-mark-active" aria-hidden="true"><span class="repair-dot" /></span>
  }
  return <span class="repair-mark repair-mark-idle" aria-hidden="true">{index + 1}</span>
}

export function RepairScreen({ routerID, checkName, lineName, onClose }) {
  const [job, setJob] = useState(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    const poll = () => {
      fetchRepairStatus(routerID)
        .then((j) => {
          if (alive) setJob(j)
        })
        .catch(() => {})
    }
    poll()
    const timer = setInterval(poll, POLL_MS)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [routerID])

  const view = repairView(job)
  const running = Boolean(job?.running)

  function start() {
    setStarting(true)
    setError('')
    startRepair(routerID, checkName)
      .then((j) => setJob(j))
      .catch((e) => setError(e?.message || 'Починку начать не удалось.'))
      .finally(() => setStarting(false))
  }

  return (
    <Overlay title="Починка" onClose={onClose}>
      <div class="repair">
        <p class="repair-title">{running ? 'Поднимаю связь' : view.title}</p>
        {running ? (
          <p class="repair-lead">Можно закрыть приложение — я допишу в чат, когда закончу.</p>
        ) : null}

        <ol class="repair-steps">
          {view.steps.map((s, i) => (
            <li key={s.key} class={`repair-step repair-step-${s.state}`}>
              <StepMark state={s.state} index={i} />
              <span class="repair-step-label">{s.label}</span>
            </li>
          ))}
        </ol>

        {view.note ? (
          <p class="repair-note">
            <Quoted text={view.note} />
          </p>
        ) : null}

        {/* Текст идёт за состоянием: «пока чиню» на законченной починке --
            неправда, а экран, который врёт в мелочи, не верят и в крупном. */}
        <div class="repair-calm">
          <p class="repair-calm-title">
            {running ? 'Пока чиню, интернет работает' : 'Интернет работал всё это время'}
          </p>
          <p class="repair-calm-text">
            {running
              ? 'Банки, госуслуги и обычные сайты идут напрямую и починки не ждут.'
              : 'Банки, госуслуги и обычные сайты идут напрямую — их починка не касалась.'}
          </p>
        </div>

        {error ? <p class="repair-error">{error}</p> : null}

        {!running ? (
          <button class="btn btn-accent repair-start" onClick={start} disabled={starting}>
            <Quoted text={starting ? 'Начинаю…' : view.done ? 'Починить ещё раз' : `Починить «${lineName || checkName}»`} />
          </button>
        ) : null}
      </div>
    </Overlay>
  )
}
