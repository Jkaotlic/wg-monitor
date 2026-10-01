// Лист «Откуда взять конфиг» (v0.52, спека §2): каждый пункт ведёт в
// существующий экран -- вкладку кабинета или загрузку .conf. Слово «кабинет» в
// навигации не встречается. Панель VPN-сервера -- только если сервер вернул
// панели (GET /routers/{id}/vpn/awg3); список не загрузился -- пункт с
// пометкой и повтором, а не молчаливое исчезновение (хвост v0.51).
export const CONFIG_SOURCES_TITLE = 'Откуда взять конфиг'

const AWG3_LABEL = 'Панель VPN-сервера'

export function configSourceChoices({ isAdmin = false, canImport = false, awg3 = { status: 'loading', panels: [] } } = {}) {
  const out = [
    { value: 'amnezia', label: 'Amnezia' },
    { value: 'hidemy', label: 'HideMy' },
  ]
  if (isAdmin) out.push({ value: 'selfhosted', label: 'Свой сервер' })
  if (awg3?.status === 'ok' && (awg3.panels ?? []).length > 0) out.push({ value: 'awg3', label: AWG3_LABEL })
  else if (awg3?.status === 'error') out.push({ value: 'awg3-retry', label: AWG3_LABEL, pill: { tone: 'warn', text: 'не загрузилось — повторить' } })
  if (canImport) out.push({ value: 'conf', label: 'Загрузить .conf' })
  return out
}

const CABINET_TABS = ['amnezia', 'hidemy', 'selfhosted', 'awg3']

export function configSourceTarget(value) {
  if (value === 'conf') return { overlay: 'confimport' }
  if (CABINET_TABS.includes(value)) return { overlay: 'cabinet', params: { tab: value } }
  return null
}
