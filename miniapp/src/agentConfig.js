// Правка конфига агента: чистые функции отдельно от экрана.
//
// Экран перенесён из операторского дашборда по решению оператора, и перенос
// сдвигает границу доверия -- поэтому запреты здесь важнее вёрстки:
//
//   1. Секреты не показываются и не уезжают назад. Путь к своему DNS-серверу
//      приезжает с роутера уже замаскированным (https://<host>/***), и
//      экран показывает только «задано». Отправка строки с «***» назад
//      отклоняется: маска затёрла бы настоящий секрет на роутере. Тот же
//      отказ стоит на бэкенде и на агенте -- это третье повторение, а не
//      единственная проверка.
//   2. Адрес бэкенда не правится ни под каким именем. Его белый список
//      живёт на стороне агента, и перенаправление адреса -- захват всего
//      парка, а не правка настройки.
//   3. Четыре ключа DNS-сторожа из мини-аппа в этой версии не правятся:
//      среди них выключатель сторожа, а ворота его первого включения этот
//      цикл не делает. Мини-апп правит семь ключей из одиннадцати.
//   4. Экран для роутера с агентом ниже пола версии не рисуется вовсе -- это
//      ВТОРАЯ преграда, независимая от гейта бэкенда. Ни одна из двух не
//      заменяет другую: бэкенд отказывается ставить команду в очередь, а
//      экран не предлагает того, чего роутер не умеет.

import { humanAge } from './labels.js'

// Пол версии агента. Старый агент не знает про новые поля и сделает не то,
// что человек прочитал на экране.
export const AGENT_CONFIG_MIN_VERSION = 'v0.31.0'

export const AGENT_CONFIG_TEXTS = {
  watchdogHidden: 'Путь к своему DNS-серверу мы не показываем — видно только «задано».',
  maskedBack: 'Чтобы изменить, впишите настоящий адрес целиком. Скрытое значение вписать назад нельзя.',
  panelPassword: 'Пароль панели роутера мы не знаем и не храним — его здесь нет.',
  restart: 'Агент перезапустится, и роутер замолчит на несколько секунд — это нормально.',
  tooOld: 'Эта настройка появится после обновления агента.',
  notEditable: 'Эту настройку из приложения не меняют.',
  nothingChanged: 'Ничего не изменилось.',
  adminOnly: 'Настройки агента меняет админ бота.',
  watchdogElsewhere: 'Настройки своего DNS-сервера правятся не здесь.',
}

// Подтверждение набором имени роутера: единственное место, где человек
// читает последствие до того, как оно случится.
export function agentConfigConfirmBody(routerName) {
  return `Наберите имя роутера «${routerName}», чтобы подтвердить изменение настроек агента. ${AGENT_CONFIG_TEXTS.restart}`
}

// Семь ключей, которые правит мини-апп. Порядок -- порядок формы: сначала
// то, что меняют часто, потом разрешения на действия с самим устройством.
const FIELDS = [
  { key: 'interval_sec', title: 'Как часто роутер отчитывается', kind: 'int', unit: 'сек', min: 10, max: 86400 },
  { key: 'external_reach_enabled', title: 'Проверять выход в интернет', kind: 'bool' },
  { key: 'external_reach_fail_threshold', title: 'Считать выход потерянным после', kind: 'int', unit: 'провалов', min: 1, max: 20 },
  { key: 'awgm_base_url', title: 'Адрес панели роутера', kind: 'string' },
  { key: 'awgm_login', title: 'Логин в панели роутера', kind: 'string', max: 64 },
  { key: 'allow_router_reboot', title: 'Разрешить перезагрузку роутера', kind: 'bool' },
  { key: 'allow_firmware_install', title: 'Разрешить установку прошивки', kind: 'bool' },
]

export function agentConfigFields() {
  return FIELDS.map((f) => ({ ...f }))
}

export function editableAgentConfigKeys() {
  return FIELDS.map((f) => f.key)
}

// Версия агента -- отказ по умолчанию: пустая, нечитаемая и неполная версия
// ЗАПРЕЩАЮТ, а не разрешают. Встречная к бэкенду проверка (agentAtLeast в
// internal/backend/agent_version.go), и обе нужны: эта закрывает экран, та
// закрывает очередь.
export function agentAtLeast(version, floor = AGENT_CONFIG_MIN_VERSION) {
  const v = parseAgentVersion(version)
  const f = parseAgentVersion(floor)
  if (!v || !f) return false
  for (const part of ['major', 'minor', 'patch']) {
    if (v[part] !== f[part]) return v[part] > f[part]
  }
  // Предрелиз ниже своего релиза: v0.31.0-rc1 -- ещё не v0.31.0.
  if (v.pre && !f.pre) return false
  return true
}

function parseAgentVersion(s) {
  const m = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(String(s ?? '').trim())
  if (!m) return null
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]), pre: m[4] ?? '' }
}

// Кому и когда экран доступен вообще. Радиус правки router-global, поэтому
// круг -- только админ бота; расширение круга -- отдельный пункт бэклога.
export function agentConfigAvailable(settings) {
  if (!settings || settings.role !== 'admin') return false
  return agentAtLeast(settings.agent_version, AGENT_CONFIG_MIN_VERSION)
}

// Ответ агента (actions.AgentConfigView) -- JSON, а не текст.
export function parseAgentConfig(output) {
  try {
    const view = JSON.parse(output)
    return view && typeof view === 'object' ? view : null
  } catch {
    return null
  }
}

// Строки «что сейчас на роутере». Секреты сюда не попадают ни в значении, ни
// в подписи: путь своего DNS-сервера сворачивается в «задано» до того, как
// станет строкой экрана.
export function agentConfigRows(view = {}) {
  const v = view ?? {}
  return [
    {
      key: 'interval',
      title: 'Как часто роутер отчитывается',
      value: v.interval_sec ? humanAge(v.interval_sec) : 'не задано',
    },
    {
      key: 'external_reach',
      title: 'Проверка выхода в интернет',
      value: v.external_reach_enabled ? 'включена' : 'выключена',
    },
    {
      key: 'external_reach_threshold',
      title: 'Считаем выход потерянным после',
      value: v.external_reach_fail_threshold ? `${v.external_reach_fail_threshold} провалов` : 'не задано',
    },
    {
      key: 'awgm_url',
      title: 'Адрес панели роутера',
      value: v.awgm_base_url || 'не задан',
    },
    {
      key: 'awgm_login',
      title: 'Логин в панели роутера',
      value: v.awgm_login || 'не задан',
    },
    {
      key: 'allow_reboot',
      title: 'Перезагрузка роутера',
      value: v.allow_router_reboot ? 'разрешена' : 'запрещена',
    },
    {
      key: 'allow_firmware',
      title: 'Установка прошивки',
      value: v.allow_firmware_install ? 'разрешена' : 'запрещена',
    },
    {
      key: 'watchdog',
      title: 'Сторож своего DNS-сервера',
      value: v.dns_watchdog_enabled ? 'включён' : 'выключен',
    },
    {
      // Значение здесь -- признак, а не адрес: сам путь открывает чужой
      // резолвер, и на экране его нет ни в каком виде.
      key: 'watchdog_endpoint',
      title: 'Свой DNS-сервер',
      value: v.dns_watchdog_endpoint ? 'задано' : 'не задано',
    },
  ]
}

// Что уедет на роутер. Только изменённое и только из семи ключей: поле вне
// белого списка и любое скрытое значение отбрасываются молча -- так же, как
// это делает агент (normalizeAgentConfigChanges).
export function agentConfigArgs(current = {}, values = {}) {
  const out = {}
  for (const f of FIELDS) {
    if (!(f.key in values)) continue
    let next = values[f.key]
    if (f.kind === 'int') {
      const n = Number(String(next).trim())
      if (!Number.isInteger(n)) continue
      if (n === Number(current[f.key])) continue
      out[f.key] = n
      continue
    }
    if (f.kind === 'bool') {
      const b = Boolean(next)
      if (b === Boolean(current[f.key])) continue
      out[f.key] = b
      continue
    }
    next = String(next ?? '').trim()
    // Скрытое значение назад не уезжает: оно затёрло бы настоящий секрет.
    if (next.includes('***')) continue
    if (next === String(current[f.key] ?? '')) continue
    out[f.key] = next
  }
  return out
}

// Проверка формы повторяет то, что проверяют бэкенд и агент. Повторяет
// намеренно: человек должен увидеть отказ до того, как команда уйдёт на
// роутер и перезапустит агента.
export function validateAgentConfig(values = {}) {
  // Скрытое значение -- первым вопросом и для любого поля: маска,
  // отправленная назад, стирает секрет на роутере.
  for (const raw of Object.values(values ?? {})) {
    if (typeof raw === 'string' && raw.includes('***')) {
      return { error: AGENT_CONFIG_TEXTS.maskedBack }
    }
  }
  const editable = editableAgentConfigKeys()
  for (const key of Object.keys(values ?? {})) {
    if (!editable.includes(key)) return { error: AGENT_CONFIG_TEXTS.notEditable }
  }
  for (const f of FIELDS) {
    if (!(f.key in values)) continue
    const raw = values[f.key]
    if (f.kind === 'int') {
      const n = Number(String(raw).trim())
      if (!Number.isInteger(n) || n < f.min || n > f.max) {
        return { error: `«${f.title}» — целое число от ${f.min} до ${f.max}.` }
      }
      continue
    }
    if (f.kind === 'bool') {
      if (typeof raw !== 'boolean') return { error: `«${f.title}» — это да или нет.` }
      continue
    }
    const s = String(raw ?? '').trim()
    if (f.key === 'awgm_base_url' && s !== '' && !/^https?:\/\/[^\s/]+/i.test(s)) {
      return { error: 'Адрес панели роутера начинается с http:// или https://.' }
    }
    if (f.max && s.length > f.max) {
      return { error: `«${f.title}» — не длиннее ${f.max} символов.` }
    }
  }
  return { error: '' }
}
