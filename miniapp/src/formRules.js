// Правила полей, общие для экранов цикла 2 (мастер «Добавить роутер»,
// «Другая версия агента», перенаправление). Проверка на экране повторяет
// сервер, а не заменяет его: смысл -- сказать «так нельзя» до отправки,
// а не после отказа.

// internal/agent/config.go:18 и invalid_nickname на сервере.
export const NICKNAME_RE = /^[a-z][a-z0-9_-]{1,15}$/
export const NICKNAME_RULE = 'Имя роутера: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.'

// Теги релизов проекта: v0.36.0 и v0.36.0-rc3.
export const AGENT_VERSION_RE = /^v\d+\.\d+\.\d+(-rc\d+)?$/

function trimmed(v) {
  return typeof v === 'string' ? v.trim() : ''
}

export function validNickname(v) {
  return NICKNAME_RE.test(trimmed(v))
}

export function validAgentVersion(v) {
  return AGENT_VERSION_RE.test(trimmed(v))
}

// Набранная версия в виде сервера: «0.36.0» дописывается до «v0.36.0»;
// негодное -- пустая строка. Одна на мастер и листы.
export function normalizeAgentVersion(v) {
  const s = trimmed(v)
  if (!s) return ''
  const withV = /^\d/.test(s) ? `v${s}` : s
  return AGENT_VERSION_RE.test(withV) ? withV : ''
}

// Как validateDashboardAWGMURL на сервере: абсолютный http(s) с хостом.
export function validHttpURL(v) {
  const s = trimmed(v)
  if (!s) return false
  let u
  try {
    u = new URL(s)
  } catch {
    return false
  }
  return (u.protocol === 'https:' || u.protocol === 'http:') && u.host !== ''
}
