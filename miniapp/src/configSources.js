// Лист «Откуда взять конфиг» (v0.52, спека §2): каждый пункт ведёт в
// существующий экран -- вкладку кабинета или загрузку .conf. Слово «кабинет» в
// навигации не встречается. Панель VPN-сервера -- только если сервер вернул
// панели (GET /routers/{id}/vpn/awg3); список не загрузился -- пункт с
// пометкой и повтором, а не молчаливое исчезновение (хвост v0.51).
export const CONFIG_SOURCES_TITLE = 'Откуда взять конфиг'

const AWG3_LABEL = 'Панель VPN-сервера'
const CONF_LABEL = 'Загрузить .conf'

const LOADING = { tone: 'muted', text: 'загружается' }
const RETRY = { tone: 'warn', text: 'не загрузилось — повторить' }

// roleStatus -- прочиталась ли роль (loading | ok | error): без неё нельзя
// сказать, есть ли право на .conf, и пункт не пропадает молча (финал п. 4).
export function configSourceChoices({ isAdmin = false, canImport = false, roleStatus = 'ok', awg3 = { status: 'loading', panels: [] } } = {}) {
  const out = [
    { value: 'amnezia', label: 'Amnezia' },
    { value: 'hidemy', label: 'HideMy' },
  ]
  if (isAdmin) out.push({ value: 'selfhosted', label: 'Свой сервер' })
  if (awg3?.status === 'ok' && (awg3.panels ?? []).length > 0) out.push({ value: 'awg3', label: AWG3_LABEL })
  else if (awg3?.status === 'error') out.push({ value: 'awg3-retry', label: AWG3_LABEL, pill: RETRY })
  else if (awg3?.status === 'loading') out.push({ value: 'awg3-loading', label: AWG3_LABEL, disabled: true, pill: LOADING })
  if (roleStatus === 'error') out.push({ value: 'conf-retry', label: CONF_LABEL, pill: RETRY })
  else if (roleStatus === 'loading') out.push({ value: 'conf-loading', label: CONF_LABEL, disabled: true, pill: LOADING })
  else if (canImport) out.push({ value: 'conf', label: CONF_LABEL })
  return out
}

const CABINET_TABS = ['amnezia', 'hidemy', 'selfhosted', 'awg3']

export function configSourceTarget(value) {
  if (value === 'conf') return { overlay: 'confimport' }
  if (CABINET_TABS.includes(value)) return { overlay: 'cabinet', params: { tab: value } }
  return null
}
