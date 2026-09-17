// «Другая версия…» в строке Парка: своя версия агента, в том числе откат.
// Здесь только чистые функции -- экран собирает из них лист.
//
// Правила листа (спека, п. 6):
//   1. Выпуска новее бэкенда нет: зеркало /v1/releases/download раздаёт то,
//      что зеркалит бэкенд, и сервер ответит no_release.
//   2. Версия ниже текущей -- откат; кнопка загорается только с включённым
//      «Это откат — разрешить» и набранным именем роутера.
//   3. Та же версия -- нечего ставить: переустановка -- отдельная кнопка.
// Сравнение здесь -- только чтобы вовремя спросить; решает сервер
// (downgrade_rejected без allow_downgrade).

import { validAgentVersion } from './formRules.js'
import { agentUpdateErrorText } from './agentUpdate.js'

// Разбор -- ради сравнения по числам; годность набранного решает общее
// правило formRules.js (vN.N.N и vN.N.N-rcN), то же, что у мастера.
const RE = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/

export function parseVersion(s) {
  const m = RE.exec(String(s ?? '').trim())
  if (!m) return null
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]), pre: m[4] ?? '' }
}

export function normalizeVersion(s) {
  const p = parseVersion(s)
  if (!p) return ''
  return `v${p.major}.${p.minor}.${p.patch}${p.pre ? `-${p.pre}` : ''}`
}

export function compareVersions(a, b) {
  const pa = parseVersion(a)
  const pb = parseVersion(b)
  if (!pa || !pb) return null
  for (const part of ['major', 'minor', 'patch']) {
    if (pa[part] !== pb[part]) return pa[part] > pb[part] ? 1 : -1
  }
  if (pa.pre === pb.pre) return 0
  if (!pa.pre) return 1
  if (!pb.pre) return -1
  // rc10 новее rc9: номер предрелиза -- число, а не строка.
  const na = /^rc(\d+)$/.exec(pa.pre)
  const nb = /^rc(\d+)$/.exec(pb.pre)
  if (na && nb) return Number(na[1]) === Number(nb[1]) ? 0 : Number(na[1]) > Number(nb[1]) ? 1 : -1
  return pa.pre < pb.pre ? -1 : 1
}

export function versionPick(input, { current = '', backend = '' } = {}) {
  const example = normalizeVersion(backend) || 'v0.36.0'
  const raw = String(input ?? '').trim()
  if (!raw) return { version: '', state: 'empty', ok: false, downgrade: false, hint: `Наберите версию агента, например ${example}.` }
  const version = normalizeVersion(raw)
  if (!version || !validAgentVersion(version)) return { version: '', state: 'invalid', ok: false, downgrade: false, hint: `Версия пишется так: ${example}.` }
  if (backend && compareVersions(version, backend) === 1) {
    return { version, state: 'ahead', ok: false, downgrade: false, hint: `Выпуска новее бэкенда (${backend}) нет.` }
  }
  const cmp = compareVersions(version, current)
  if (cmp === 0) return { version, state: 'same', ok: false, downgrade: false, hint: `На роутере уже ${current}.` }
  if (cmp === -1) return { version, state: 'downgrade', ok: true, downgrade: true, hint: `Это откат: на роутере ${current}.` }
  return { version, state: 'upgrade', ok: true, downgrade: false, hint: cmp === 1 ? `Обновление с ${current}.` : '' }
}

// Не предлагаем, пока агент и так меняется: ждёт отложенное обновление или
// идёт оживление -- две цели одному роутеру разом сервер не развёл бы.
const REVIVE_BUSY = new Set(['waiting', 'running'])

export function canPickVersion(router) {
  return Boolean(parseVersion(router?.agent_version)) && !router?.pending_version && !REVIVE_BUSY.has(router?.revive?.status)
}

export function otherVersionSheetText(router, backend) {
  const name = router?.nickname ?? ''
  const current = router?.agent_version || 'версия неизвестна'
  return {
    title: `Поставить другую версию агента на «${name}»?`,
    body:
      `Сейчас на роутере ${current}. Выпуск берётся с зеркала этого сервера, поэтому версия — не новее ${backend || 'версии бэкенда'}. ` +
      'Откат на более старую версию нужно разрешить отдельно. Агент перезапустится, проверки на минуту замолчат.',
  }
}

function pickFor(values, router, backend) {
  return versionPick(values?.target_version, { current: router?.agent_version ?? '', backend })
}

// memo -- общее состояние одного листа: memo.rejected ставит отказ сервера
// downgrade_rejected. Сервер сравнивает с last_deployed_version, клиент -- с
// agent_version, и они могут разойтись: после такого отказа откат нужно
// разрешить и для версии, которую клиент считал обновлением.
function needsAllow(p, memo) {
  return p.ok && (p.downgrade || memo?.rejected === true)
}

export function otherVersionFields(router, backend, memo = {}) {
  return [
    {
      name: 'target_version',
      label: 'Версия агента',
      type: 'text',
      keep: true,
      placeholder: normalizeVersion(backend) || 'v0.36.0',
      hint: (values) => pickFor(values, router, backend).hint,
    },
    {
      name: 'allow_downgrade',
      label: 'Это откат — разрешить',
      type: 'toggle',
      initial: false,
      showIf: (values) => needsAllow(pickFor(values, router, backend), memo),
    },
  ]
}

export function otherVersionReady(router, backend, memo = {}) {
  return (values = {}) => {
    const p = pickFor(values, router, backend)
    if (!p.ok) return false
    return !needsAllow(p, memo) || values.allow_downgrade === true
  }
}

export function otherVersionRequest(values, router, backend, memo = {}) {
  const p = pickFor(values, router, backend)
  return { targetVersion: p.version, allowDowngrade: needsAllow(p, memo) && values?.allow_downgrade === true }
}

// Тексты отказов листа: downgrade_rejected здесь -- просьба разрешить откат
// (и открывает переключатель), остальное -- как у «Обновить агент».
export function otherVersionErrorText(memo = {}) {
  return (err) => {
    if (err?.code === 'downgrade_rejected') {
      memo.rejected = true
      return 'Это откат версии — подтвердите откат.'
    }
    return agentUpdateErrorText(err)
  }
}
