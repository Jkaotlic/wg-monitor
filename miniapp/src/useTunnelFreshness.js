import { useEffect, useState } from 'preact/hooks'
import { PULSE_MS } from './pulse.js'

export function useTunnelFreshness(clockOffsetMs) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const page = document
    const update = () => {
      if (!page.hidden) setTick((tick) => tick + 1)
    }
    const timer = setInterval(update, PULSE_MS)
    page.addEventListener('visibilitychange', update)
    return () => {
      clearInterval(timer)
      page.removeEventListener('visibilitychange', update)
    }
  }, [])
  return { enforceFreshness: true, requireTimestamp: true, nowMs: Date.now() + (clockOffsetMs ?? 0) }
}
