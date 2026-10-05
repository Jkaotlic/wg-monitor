// Автопочинка VPN-туннеля: тексты метки, строки экрана и листа включения.
// Экраны (TunnelScreen, TunnelsTab) только рисуют то, что собрано здесь.
//
// Ответ бэкенда (GET .../tunnels/{id}/autorepair): enabled, provider, option,
// allow_relocate, suggested {provider, option, why}, sources [{provider, label,
// options [{id, label}], ok, note}], blocked (строка), rename_pending (прежнее
// имя, если VPN-туннель переименовали после включения), can_edit. Запасного
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

// reason -- причина стопа из списка (reasons): сейчас она бывает одна --
// VPN-туннель переименован и ждёт подтверждения автопочинки.
export function autorepairBadge(state, reason = '') {
  if (state === 'on') return { tone: 'ok', text: 'Автопочинка' }
  if (state === 'limited') return { tone: 'neutral', text: 'Автопочинка: только перезапуск' }
  if (state === 'blocked' && reason) return { tone: 'warn', text: 'Автопочинка ждёт подтверждения' }
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
    hint = 'Выключить можно кнопкой «Выключить автопочинку» ниже, в любой момент.'
    if (r.rename_pending) {
      // Переименовали после включения: перезапуск идёт, выпуск ждёт человека.
      value = 'ждёт подтверждения'
      hint = `VPN-туннель переименован (был ${quote(r.rename_pending)}): пока вы не подтвердите автопочинку, я только перезапускаю его, конфиг не выпускаю.`
    } else if (r.blocked) value = `стоит: ${r.blocked}`
    else if (!r.provider || !r.option) value = 'только перезапуск'
    else value = `включена · из ${sourceLabel(r.sources, r.provider, r.option)}`
  }
  return { title: 'Автопочинка', value, hint }
}

// Запасной VPN-туннель из снимка роутера: первый доступный другой туннель в
// наборе, где стоит этот. Снимок неизвестен -- known:false (фразу о резерве
// лист опускает); известен, но резерва нет -- known:true и пустое имя.
//
// Уводит трафик бэкенд только с первого звена цепочки: оно и несёт трафик,
// пока живо. Этот VPN-туннель стоит в цепочке не первым -- он сам резерв:
// carrier -- имя звена, через которое трафик идёт сейчас, и обещать «уведу
// на запасной» нельзя. reserve -- этот случай; carrier пустой при reserve --
// живого звена нет (первое тоже лежит), и называть лежащее нельзя.
//
// Снимок сюда приходит с вердиктом проверок (withCheckVerdict): звено, чья
// проверка провалена (status 'dead'), не живое, даже если интерфейс поднят, --
// главный экран такой VPN-туннель несущим тоже не называет. self -- этот
// VPN-туннель резерв, но трафик сейчас идёт через него самого.
function linkAvailable(i) {
  if (typeof i.available === 'boolean') return i.available
  return Boolean(i.role) && i.role !== 'unavailable' && i.role !== 'down'
}

export function backupFor(snapshot, tunnelID) {
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  if (tunnels.length === 0 || !Array.isArray(snapshot?.policies)) return { known: false, name: '', carrier: '', reserve: false, self: false }
  const metaOf = (link) => tunnels.find((t) => link.tunnel_id && t.id === link.tunnel_id)
  const nameOf = (link) => String(metaOf(link)?.name || link.name || link.tunnel_id || link.bind || '').trim()
  const alive = (link) => linkAvailable(link) && metaOf(link)?.status !== 'dead'
  for (const p of snapshot.policies) {
    const links = Array.isArray(p.interfaces) ? p.interfaces : []
    if (!links.some((i) => i.tunnel_id === tunnelID) && p.active_tunnel_id !== tunnelID) continue
    if (links.length > 0 && links[0].tunnel_id !== tunnelID && links.some((i) => i.tunnel_id === tunnelID)) {
      const activeID = p.active_tunnel_id || links.find((i) => i.role === 'active')?.tunnel_id
      // Несёт трафик сам -- только если сам жив: иначе он и есть «нет рабочего».
      const selfLink = links.find((i) => i.tunnel_id === tunnelID)
      if (activeID === tunnelID && selfLink && alive(selfLink)) return { known: true, name: '', carrier: '', reserve: true, self: true }
      const live = (i) => i.tunnel_id !== tunnelID && alive(i)
      const carrier = links.find((i) => i.tunnel_id === activeID && live(i)) || links.find(live)
      return { known: true, name: '', carrier: carrier ? nameOf(carrier) : '', reserve: true, self: false }
    }
    const other = links.find((i) => i.tunnel_id && i.tunnel_id !== tunnelID && alive(i))
    if (!other) continue
    return { known: true, name: nameOf(other), carrier: '', reserve: false, self: false }
  }
  return { known: true, name: '', carrier: '', reserve: false, self: false }
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
// запасного) или null (не знаем). reserve -- этот VPN-туннель сам резерв;
// carrier -- через что идёт трафик (пусто -- живого звена нет), см. backupFor.
//
// Источник человек выбирает в поле ниже и может сменить его до нажатия, а
// текст листа от поля не зависит: поэтому источник здесь не называется.
export function enableSheetText(resp, tunnelName, backup, carrier = '', reserve = Boolean(carrier), self = false) {
  const sources = okSources(resp)
  const name = `«${tunnelName}»`
  const reissue = sources.length > 0 ? ', а если не поможет и ниже выбран источник — выпущу конфиг заново из него и заменю конфиг на роутере' : ''
  let what
  if (reserve && self) {
    what = `Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}. Он запасной, но сейчас трафик идёт через него самого: первый VPN-туннель цепочки не отвечает, а порядок VPN-туннелей я не меняю.`
  } else if (reserve && carrier) {
    what = `Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}. Он запасной: трафик и так идёт через «${carrier}», порядок VPN-туннелей я не меняю.`
  } else if (reserve) {
    what = `Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}. Он запасной, а у трафика сейчас нет рабочего VPN-туннеля; порядок VPN-туннелей я не меняю.`
  } else if (backup === null || backup === undefined) {
    what = `Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}.`
  } else if (backup) {
    what = `Если VPN-туннель ${name} упадёт, я уведу трафик на запасной VPN-туннель «${backup}», перезапущу ${name}${reissue}.`
  } else {
    what = `Запасного VPN-туннеля нет: пока чиню, заблокированное открываться не будет. Если VPN-туннель ${name} упадёт, я перезапущу его${reissue}.`
  }
  return {
    // Переименованный VPN-туннель: тот же лист, но это подтверждение.
    title: resp?.rename_pending ? `Подтвердить автопочинку «${tunnelName}»?` : `Включить автопочинку «${tunnelName}»?`,
    sections: [
      { h: 'Что будет делать', text: what },
      // Цена своя у каждого источника, а источник человек меняет в поле:
      // она живёт в подсказке поля (costText) и следует выбору.
      { h: 'Чего стоит', text: 'Перезапуск ничего не тратит. Цена выпуска зависит от источника — она написана под полем «Откуда выпускать конфиг заново».' },
      { h: 'Кому напишу', text: 'Владельцу, операторам и админу — в личку бота: что упало, что делаю и чем кончилось. Если понадобится ваше участие, напишу отдельно, со звуком.' },
      { h: 'Как выключить', text: 'Кнопкой «Выключить автопочинку» на этом экране, в любой момент.' },
    ],
    // Про урезанный режим говорит подсказка поля источника (она следует
    // выбору); вторая такая же строка под листом -- повтор.
    note: '',
  }
}

// costText -- цена выбранного источника: у «Amnezia Premium» страна -- место в
// подписке (смена страны -- одна на настройку, relocate_spent -- уже
// израсходована), у «HideMy.name» код открывает все серверы, у своего
// сервера новое подключение -- место на сервере.
export function costText(resp, provider) {
  const src = findSource(resp?.sources, provider)
  const where = 'Смена локации меняет страну, через которую видны сайты.'
  if (provider === 'amnezia') {
    const head = `${quote(src?.label || BRAND.amnezia)}: повторный выпуск того же конфига места в подписке не тратит.`
    const spent = String(resp?.relocate_spent ?? '').trim()
    if (spent) {
      const opt = (src?.options ?? []).find((o) => o.id === spent)
      return `${head} Смену страны автопочинка этого VPN-туннеля уже использовала (${quote(opt?.label || spent)}), новых мест в подписке она больше не займёт.`
    }
    return `${head} Смена страны может один раз занять ещё одно место в подписке: выпущу одну новую страну и больше не буду. Уже выпущенные страны не беру — их ключи стоят на других устройствах, а один ключ в двух местах ломает оба. ${where}`
  }
  if (provider === 'hidemyname') return `${quote(src?.label || BRAND.hidemyname)}: повторный выпуск и смена сервера места не тратят — код открывает все серверы. ${where}`
  if (provider === 'awg3') return 'Если старое подключение на сервере не оживёт, заведу новое — оно займёт ещё одно место на сервере.'
  return ''
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
      hint: (v) => (v?.source ? costText(resp, splitPick(v.source)[0]) : NO_SOURCE_NOTE),
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
