export const TUNNEL_EVENT_FRESH_MS = 5 * 60_000

export function tunnelEventAgeSec(event, nowMs = Date.now()) {
  const at = Date.parse(event?.ts ?? '')
  if (!Number.isFinite(at) || at > nowMs + 2 * 60_000) return null
  return Math.max(0, Math.floor((nowMs - at) / 1000))
}

export function tunnelEventFresh(event, nowMs = Date.now(), maxAgeMs = TUNNEL_EVENT_FRESH_MS) {
  const age = tunnelEventAgeSec(event, nowMs)
  return age != null && nowMs - Date.parse(event.ts) <= maxAgeMs
}
