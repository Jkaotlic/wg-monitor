// Блок HydraRoute Neo в «Маршрутах»: состояние, запуск/остановка/перезапуск и
// список правил (только чтение). Экран HrneoBlock.jsx только рисует.
//
// Состояние -- из hrneo_inventory (ответ агента); не ответил (старый агент,
// отказ) -- из снимка маршрутизации (hr_neo), который экран уже держит.
import { parseRouteSnapshot, bindTunnelName } from './routes.js'
import { confirmSheet } from './sheet.js'
import { mayMaintain, restartSheet } from './maintenance.js'
import { pluralRu } from './labels.js'

export const HRNEO_TEXTS = {
  title: 'HydraRoute Neo — движок умной раздельной маршрутизации',
  rules: 'Правила HydraRoute Neo',
  readOnly: 'Только для чтения: здесь видно, что HydraRoute Neo отправляет в обход и через что.',
  loading: 'Спрашиваем роутер о правилах HydraRoute Neo…',
  oldAgent: 'Список правил HydraRoute Neo этот агент пока не отдаёт — обновите агента на роутере. Состояние показано по снимку маршрутизации.',
  failed: 'Роутер не отдал список правил HydraRoute Neo — состояние показано по снимку маршрутизации.',
  noRules: 'Своих правил у HydraRoute Neo на роутере нет.',
}

export function parseHrneoInventory(output) {
  const v = parseRouteSnapshot(output)
  if (!v || !v.status || typeof v.status !== 'object') return null
  return {
    installed: v.status.installed === true,
    running: v.status.running === true,
    rules: Array.isArray(v.rules) ? v.rules : [],
  }
}

export function hrneoState({ inventory, snapshot } = {}) {
  if (inventory) return { known: true, installed: inventory.installed, running: inventory.running, source: 'inventory' }
  const hr = snapshot?.hr_neo
  if (hr && typeof hr === 'object') return { known: true, installed: hr.installed === true, running: hr.running === true, source: 'snapshot' }
  return { known: false, installed: false, running: false, source: 'none' }
}

export function hrneoStatusLine(state) {
  if (!state?.known) return { tone: 'muted', chip: 'неизвестно', text: 'Роутер пока не сказал, установлен ли HydraRoute Neo.' }
  if (!state.installed) return { tone: 'muted', chip: 'не установлен', text: 'HydraRoute Neo на роутере не установлен.' }
  if (state.running) return { tone: 'ok', chip: 'работает', text: 'Установлен и запущен: правила по имени сайта действуют.' }
  return { tone: 'danger', chip: 'остановлен', text: 'Установлен, но остановлен: правила по имени сайта не работают, пока его не запустят.' }
}

// Запуск и остановка -- владелец и админ (спека, решение 1). Перезапуск --
// тем же кругом, что в Настройках (mayMaintain): право уже было, отнимать его
// переездом кнопки незачем.
export function mayControlHrneo(role) {
  return role === 'admin' || role === 'owner'
}

export function hrneoActions(state, role) {
  if (!state?.known || !state.installed) return []
  const out = []
  if (state.running) {
    if (mayMaintain({ role })) out.push({ name: 'hrneo', label: 'Перезапустить', danger: false })
    if (mayControlHrneo(role)) out.push({ name: 'hrneo_stop', label: 'Остановить', danger: true })
  } else if (mayControlHrneo(role)) {
    out.push({ name: 'hrneo_start', label: 'Запустить', danger: false })
  }
  return out
}

const CONTROL = {
  hrneo_start: {
    title: 'Запустить HydraRoute Neo?',
    body: 'HydraRoute Neo запустится, и правила по имени сайта снова начнут работать.',
    button: 'Запустить',
    label: 'запуск HydraRoute Neo',
    danger: false,
  },
  hrneo_stop: {
    title: 'Остановить HydraRoute Neo?',
    body: 'Правила по имени сайта перестанут работать до запуска. Запустить HydraRoute Neo снова можно здесь же.',
    button: 'Остановить',
    label: 'остановка HydraRoute Neo',
    danger: true,
  },
}

export function hrneoSheet({ routerID, name, asleep = false, onResult }) {
  if (name === 'hrneo') return restartSheet({ routerID, name, asleep, onResult })
  const t = CONTROL[name]
  if (!t) throw new Error(`unknown HydraRoute Neo action: ${name}`)
  return confirmSheet({
    routerID,
    title: t.title,
    body: t.body,
    action: 'service_restart',
    args: { name },
    buttonLabel: t.button,
    commandLabel: t.label,
    danger: t.danger,
    asleep,
    onResult,
  })
}

// Строка правила: сколько сайтов и сетей, куда ведёт, выключено ли. Привязка
// «policy:X» -- общий набор, а не VPN-туннель.
export function hrneoRuleRows(inventory, snapshot) {
  return (inventory?.rules ?? []).map((r, i) => {
    const sites = (r.domains?.length ?? 0) + (r.manual_domains?.length ?? 0)
    const nets = r.routes?.length ?? 0
    const parts = []
    if (sites > 0) parts.push(`${sites} ${pluralRu(sites, 'сайт', 'сайта', 'сайтов')}`)
    if (nets > 0) parts.push(`${nets} ${pluralRu(nets, 'адрес сети', 'адреса сетей', 'адресов сетей')}`)
    const bind = String(r.bind ?? '')
    if (r.policy_name) parts.push(`общий набор «${r.policy_name}»`)
    else if (bind.startsWith('policy:')) parts.push(`общий набор «${bind.slice('policy:'.length)}»`)
    else if (bind) parts.push(`через «${bindTunnelName(snapshot, bind)}»`)
    if (r.enabled === false) parts.push('выключено')
    return { id: r.id || `rule-${i}`, title: r.name || r.id || 'правило без имени', sub: parts.join(' · ') }
  })
}

export function inventoryNote({ busy, result, error, inventory } = {}) {
  if (inventory) return inventory.installed && inventory.rules.length === 0 ? HRNEO_TEXTS.noRules : ''
  if (busy) return HRNEO_TEXTS.loading
  if (result && /^unknown action:/i.test(String(result.output ?? '').trim())) return HRNEO_TEXTS.oldAgent
  if (error || result) return HRNEO_TEXTS.failed
  return ''
}
