import { useEffect, useState } from 'preact/hooks'
import { fetchHealth } from '../api.js'
import { DEPLOY_RELOAD_MS, deployWaitStart, deployWaitText, watchBackendDeploy } from '../backendDeploy.js'

const REAL_CLOCK = {
  now: () => Date.now(),
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
}

// Полноэкранное «Бэкенд обновляется до vX». Слой закреплён (nav.js,
// PINNED_OVERLAYS): «назад» и Esc его не закрывают -- без сервера приложению
// некуда вернуться. Выход -- перезагрузка при новой версии или «Вернуться»
// после 5 минут.
export function BackendDeployWait({ targetVersion, onBack, reload = () => window.location.reload(), clock = REAL_CLOCK }) {
  const [state, setState] = useState(() => deployWaitStart(targetVersion, clock.now()))

  useEffect(() => {
    const signal = { cancelled: false }
    watchBackendDeploy({
      target: targetVersion,
      fetchHealth,
      sleep: clock.sleep,
      now: clock.now,
      signal,
      onState: (s) => {
        if (!signal.cancelled) setState(s)
      },
    }).then((final) => {
      if (signal.cancelled || final.phase !== 'done') return
      clock.sleep(DEPLOY_RELOAD_MS).then(() => {
        if (!signal.cancelled) reload()
      })
    })
    return () => {
      signal.cancelled = true
    }
  }, [targetVersion])

  const text = deployWaitText(state)
  return (
    <div class="deploy-wait" role="dialog" aria-modal="true" aria-labelledby="deploy-wait-title">
      <div class={`deploy-wait-card deploy-wait-${text.tone}`}>
        <span class="deploy-wait-pulse" aria-hidden="true" />
        <h1 id="deploy-wait-title" class="deploy-wait-title">
          {text.title}
        </h1>
        <p class="deploy-wait-line" role="status">
          {text.line}
        </p>
        {state.phase === 'timeout' && (
          <button type="button" class="btn btn-primary" onClick={onBack}>
            Вернуться
          </button>
        )}
      </div>
    </div>
  )
}
