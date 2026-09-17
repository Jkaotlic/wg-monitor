import { useEffect, useRef } from 'preact/hooks'
import { Overlay } from '../ui/Overlay.jsx'
import { Quoted } from '../ui/Q.jsx'
import { useJobPoll } from '../useJobPoll.js'
import { jobReconnecting } from '../jobPoll.js'
import { jobView, JOB_TEXTS } from '../jobSteps.js'

// «Ход работы» -- один экран для установки, переустановки и перенаправления
// агента. Спрашивает сервер раз в 1,5 с, пока задание не кончится; сетевые
// сбои пережидает («Переподключение…»), 404 -- задание истекло.
//
// Интерфейс общий с частью 3 (переустановка, перенаправление): jobId и
// заголовок приходят из nav.overlayParams, onDone -- один раз на финише
// (Парк перечитывает сводку), onOpenRouter -- только при успехе и известном
// router_id.
export function JobProgress({ jobId, title, backLabel = 'Назад', onClose, onOpenRouter, onDone, sleep }) {
  const poll = useJobPoll(jobId, sleep ? { sleep } : undefined)
  const view = poll.job ? jobView(poll.job) : null

  const doneRef = useRef(false)
  useEffect(() => {
    if (view?.finished && !doneRef.current) {
      doneRef.current = true
      onDone?.(poll.job)
    }
  }, [view?.finished])

  let body
  if (poll.phase === 'denied') {
    body = <p class="job-status job-status-bad">{poll.message || JOB_TEXTS.denied}</p>
  } else if (poll.phase === 'expired') {
    body = (
      <>
        <p class="job-status job-status-bad">{JOB_TEXTS.expired}</p>
        <p class="hint">{JOB_TEXTS.expiredSub}</p>
      </>
    )
  } else if (poll.phase === 'lost') {
    body = (
      <>
        <p class="job-status job-status-bad">{JOB_TEXTS.lost}</p>
        <p class="hint">{JOB_TEXTS.lostSub}</p>
      </>
    )
  } else if (!view) {
    body = (
      <>
        <p class="state">{JOB_TEXTS.loading}</p>
        {jobReconnecting(poll) && <p class="hint job-reconnecting">{JOB_TEXTS.reconnecting}</p>}
      </>
    )
  } else {
    body = (
      <>
        <p class={`job-status job-status-${view.tone}`} role="status">
          {view.headline}
        </p>
        {jobReconnecting(poll) && <p class="hint job-reconnecting">{JOB_TEXTS.reconnecting}</p>}
        <ol class="job-steps">
          {view.steps.map((s) => (
            <li key={s.name} class={`job-step job-step-${s.status}`} aria-current={s.status === 'active' ? 'step' : undefined}>
              <span class="job-step-mark" aria-hidden="true" />
              <span class="job-step-body">
                <span class="job-step-label">{s.label}</span>
                {s.detail && <span class="job-step-detail">{s.detail}</span>}
              </span>
            </li>
          ))}
        </ol>
        {view.hint && <p class="job-hint">{view.hint}</p>}
        {view.tail && (
          <details class="job-details">
            <summary>{JOB_TEXTS.details}</summary>
            <pre class="job-tail">{view.tail}</pre>
          </details>
        )}
        {!view.finished && <p class="hint">{JOB_TEXTS.leave}</p>}
      </>
    )
  }

  const finished = poll.phase === 'expired' || poll.phase === 'lost' || poll.phase === 'denied' || Boolean(view?.finished)

  return (
    <Overlay title={title} backLabel={backLabel} onBack={onClose}>
      <div class="screen job">
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        {body}
        {finished && (
          <div class="job-actions">
            {view?.success && view.routerID != null && onOpenRouter && (
              <button type="button" class="btn btn-primary" onClick={() => onOpenRouter(view.routerID)}>
                {JOB_TEXTS.openRouter}
              </button>
            )}
            <button type="button" class="btn btn-ghost" onClick={onClose}>
              {JOB_TEXTS.close}
            </button>
          </div>
        )}
      </div>
    </Overlay>
  )
}
