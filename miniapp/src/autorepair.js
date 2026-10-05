// Автопочинка VPN-туннеля: тексты метки, строки экрана и листа включения.
// Экраны (TunnelScreen, TunnelsTab) только рисуют то, что собрано здесь.
//
// Ответ бэкенда (GET .../tunnels/{id}/autorepair): enabled, provider, option,
// allow_relocate, suggested {provider, option, why}, sources [{provider, label,
// options [{id, label}], ok, note}], blocked (строка), can_edit. Запасного
// VPN-туннеля бэкенд не знает (снимок наборов правил живёт на роутере), поэтому
// резерв считается здесь из снимка, который экран и так держит.
//
// Правило текстов владельцу: латиница вне «ёлочек» -- только «VPN». Имена
// кабинетов («Amnezia Premium», «HideMy.name») и панелей приходят от сервера
// как есть, и здесь они всегда кладутся в «ёлочки».

const CABINETS = ['amnezia', 'hidemyname']
const BRAND = { amnezia: 'Amnezia Premium', hidemyname: 'HideMy.name', awg3: 'Свой сервер' }

export const NO_SOURCE_NOTE = 'Источник не выбран: смогу только перезапускать. Чтобы пересоздавать конфиг, выберите кабинет.'

const quote = (s) => {
  const t = String(s ?? '').trim()
  return t.includes('«') ? t : `«${t}»`
}

export function autorepairBadge(state) {
  if (state === 'on') return { tone: 'ok', text: 'Автопочинка' }
  if (state === 'limited') return { tone: 'neutral', text: 'Автопочинка: только перезапуск' }
  if (state === 'blocked') return { tone: 'warn', text: 'Автопочинка стоит — нужен человек' }
  return null
}

function findSource(sources, provider) {
  return (sources ?? []).find((s) => s.provider === provider)
}

// Подпись источника словами человека, всегда с именами в «ёлочках».
// Свой сервер -- вариант уже несёт «панель» · «интерфейс» в ёлочках.
export function sourceLabel(sources, provider, option) {
  const src = findSource(sources, provider)
  const opt = (src?.options ?? []).find((o) => o.id === option)
  if (provider === 'awg3') return opt ? quote(opt.label) : option ? quote(option) : quote(src?.label || BRAND.awg3)
  return quote(src?.label || BRAND[provider] || provider)
}

// Строка «Автопочинка» экрана VPN-туннеля.
export function autorepairRow(resp) {
  const r = resp ?? {}
  let value = 'выключена'
  let hint = 'Включите, и если VPN-туннель упадёт, я починю его сам.'
  if (r.enabled) {
    hint = 'Выключить можно этим же переключателем, в любой момент.'
    if (r.blocked) value = `стоит: ${r.blocked}`
    else if (!r.provider || !r.option) value = 'только перезапуск'
    else value = `включена · из ${sourceLabel(r.sources, r.provider, r.option)}`
  }
  return { title: 'Автопочинка', value, hint }
}

// Запасной VPN-туннель из снимка роутера: первый доступный другой туннель в
// наборе, где стоит этот. Снимок неизвестен -- known:false (фразу о резерве
// лист опускает); известен, но резерва нет -- known:true и пустое имя.
function linkAvailable(i) {
  if (typeof i.available === 'boolean') return i.available
  return Boolean(i.role) && i.role !== 'unavailable'
}

export function backupFor(snapshot, tunnelID) {
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  if (tunnels.length === 0 || !Array.isArray(snapshot?.policies)) return { known: false, name: '' }
  for (const p of snapshot.policies) {
    const links = Array.isArray(p.interfaces) ? p.interfaces : []
    if (!links.some((i) => i.tunnel_id === tunnelID) && p.active_tunnel_id !== tunnelID) continue
    const other = links.find((i) => i.tunnel_id && i.tunnel_id !== tunnelID && linkAvailable(i))
    if (!other) continue
    const meta = tunnels.find((t) => t.id === other.tunnel_id)
    return { known: true, name: String(meta?.name || other.name || other.tunnel_id).trim() }
  }
  return { known: true, name: '' }
}

function okSources(resp) {
  return (resp?.sources ?? []).filter((s) => s.ok && (s.options ?? []).length > 0)
}

// Значение select: «провайдер|вариант».
const pick = (provider, option) => (provider && option ? `${provider}|${option}` : '')
const splitPick = (v) => {
  const i = String(v ?? '').indexOf('|')
  return i < 0 ? ['', ''] : [v.slice(0, i), v.slice(i + 1)]
}

function pickable(resp, provider, option) {
  const src = findSource(okSources(resp), provider)
  return Boolean(src && src.options.some((o) => o.id === option))
}

function initialPick(resp) {
  if (pickable(resp, resp?.provider, resp?.option)) return pick(resp.provider, resp.option)
  const s = resp?.suggested
  if (s && pickable(resp, s.provider, s.option)) return pick(s.provider, s.option)
  return ''
}

// name -- имя этого VPN-туннеля, backup -- имя запасного: строка, '' (нет
// запасного) или null (не знаем).
export function enableSheetText(resp, tunnelName, backup) {
  const sources = okSources(resp)
  const start = initialPick(resp)
  const [sp, so] = splitPick(start)
  const from = start ? sourceLabel(resp.sources, sp, so) : sources.length > 0 ? 'выбранного ниже источника' : ''
  const name = `«${tunnelName}»`
  const reissue = from ? `, а если не поможет — выпущу конфиг заново из ${from} и заменю его на роутере` : ''
  let what
  if (backup === null || backup === undefined) {
    what = `Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}.`
  } else if (backup) {
    what = `Если VPN-туннель ${name} упадёт, я уведу трафик на запасной VPN-туннель «${backup}», перезапущу ${name}${reissue}.`
  } else {
    what = `Запасного VPN-туннеля нет: пока чиню, заблокированное открываться не будет. Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}.`
  }
  const costs = []
  if (sources.some((s) => CABINETS.includes(s.provider))) {
    costs.push('Повторный выпуск того же конфига место в кабинете не тратит. Смена локации меняет страну, через которую видны сайты.')
  }
  if (sources.some((s) => s.provider === 'awg3')) {
    costs.push('Если старое подключение на сервере не оживёт, заведу новое — оно займёт ещё одно место на сервере.')
  }
  if (costs.length === 0) costs.push('Перезапуск ничего не тратит.')
  return {
    title: `Включить автопочинку «${tunnelName}»?`,
    sections: [
      { h: 'Что будет делать', text: what },
      { h: 'Чего стоит', text: costs.join(' ') },
      { h: 'Кому напишу', text: 'Владельцу, операторам и админу — в личку бота: что упало, что делаю и чем кончилось. Если понадобится ваше участие, напишу отдельно, со звуком.' },
      { h: 'Как выключить', text: 'Этим же переключателем, в любой момент.' },
    ],
    note: start ? '' : NO_SOURCE_NOTE,
  }
}

// Поля localSheet: источник (select; только подключённые) и галочка «можно
// сменить локацию» -- только у кабинетов (у своего сервера локации нет).
export function enableFields(resp) {
  const options = [{ value: '', label: 'Без источника — только перезапуск' }]
  for (const s of okSources(resp)) {
    for (const o of s.options) {
      const label = s.provider === 'awg3' ? quote(o.label) : `${quote(s.label || BRAND[s.provider])} · ${quote(o.label)}`
      options.push({ value: pick(s.provider, o.id), label })
    }
  }
  return [
    {
      name: 'source',
      type: 'select',
      label: 'Откуда выпускать конфиг заново',
      options,
      initial: initialPick(resp),
      hint: (v) => (v?.source ? '' : NO_SOURCE_NOTE),
    },
    {
      name: 'allow_relocate',
      type: 'toggle',
      label: 'Можно сменить локацию, если прежняя не откроется',
      showIf: (v) => CABINETS.includes(splitPick(v?.source)[0]),
    },
  ]
}

// Без источника включение остаётся возможным (урезанный режим), поэтому
// кнопка всегда готова; меняется только пояснение под полем.
export function enableReady() {
  return true
}

export function enableBody(values) {
  const [provider, option] = splitPick(values?.source)
  return { enabled: true, provider, option, allow_relocate: CABINETS.includes(provider) && values?.allow_relocate === true }
}
