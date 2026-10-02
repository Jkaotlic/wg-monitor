// Машинная приёмка вёрстки (v0.52, спека §9). collectLayout уходит в
// page.evaluate целиком -- поэтому без замыканий и импортов; оценщики --
// чистые функции над тем, что он собрал (их проверяет vitest).

// Машинные коды (awg10, check_direct, сырой ответ) -- 11 px по замыслу (v0.50).
export const SMALL_OK = ['.data-row-code', '.tunnel-id', '.ev-code', '.raw-dump']
// Системная кнопка «назад» заглушки Telegram в песочнице -- не часть приложения.
export const SKIP_TARGETS = '#sandbox-back'

export function collectLayout({ smallOk = [], skip = '' } = {}) {
  // Что видит человек: открыт лист -- только он; открыт слой -- верхний слой
  // (и колонка широкой раскладки); иначе -- вся страница.
  const sheet = document.querySelector('.sheet')
  // .deploy-wait -- закреплённое ожидание раскатки бэкенда: закрывает страницу целиком.
  const overlays = [...document.querySelectorAll('.overlay, .deploy-wait')]
  const roots = sheet ? [sheet] : overlays.length ? [overlays[overlays.length - 1], ...document.querySelectorAll('.side')] : [document.body]
  const inScope = (el) => roots.some((r) => r.contains(el))
  const visible = (el) => {
    if (!inScope(el) || (skip && el.closest(skip))) return false
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) return false
    const cs = getComputedStyle(el)
    if (cs.visibility === 'hidden' || cs.display === 'none' || Number(cs.opacity) === 0) return false
    return el.offsetParent !== null || cs.position === 'fixed'
  }
  const label = (el) => (el.innerText || el.getAttribute('aria-label') || el.getAttribute('title') || '').trim().replace(/\s+/g, ' ').slice(0, 60)
  const sel = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).slice(0, 3).join('.') : '')

  // Цели касания: кнопки, ссылки, вкладки, чипы, раскрывашки, переключатели
  // (label у чекбокса/радио, сами чекбоксы и радио, select).
  const isToggleLabel = (el) => el.tagName === 'LABEL' && Boolean(el.querySelector('input[type=checkbox], input[type=radio]') || ['checkbox', 'radio'].includes(el.control?.type))
  const targets = [...document.querySelectorAll('button, a[href], [role=button], [role=tab], summary, .strip-chip, label, select, input[type=checkbox], input[type=radio]')]
    .filter((el) => (el.tagName === 'LABEL' ? isToggleLabel(el) : el.tagName === 'INPUT' ? !el.closest('label') : true))
    .filter(visible)
    .map((el) => {
      const r = el.getBoundingClientRect()
      return { text: label(el), sel: sel(el), w: Math.round(r.width), h: Math.round(r.height) }
    })

  const limes = [...document.querySelectorAll('.btn-primary')].filter(visible).map(label)

  const smallText = []
  const okSel = smallOk.join(',')
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n.textContent.trim()
    const el = n.parentElement
    if (!t || !el || !visible(el) || (okSel && el.closest(okSel))) continue
    const size = parseFloat(getComputedStyle(el).fontSize)
    if (size < 12) smallText.push({ text: t.slice(0, 40), size, sel: sel(el) })
  }

  // Обрезан. Три способа, итог -- объединение без повторов:
  //  1) элемент со своим текстом, чей текст шире его самого (без своей прокрутки
  //     и многоточия по замыслу);
  //  2) любой видимый элемент с overflow-x hidden/clip, у которого содержимое шире
  //     коробки, если внутри есть текст;
  //  3) строчный владелец текста (inline: свои ширины нулевые): прямоугольник
  //     текста (Range) выходит за ближайшего предка с overflow hidden/clip.
  const clipped = []
  const seenClip = new Set()
  const pushClip = (el, sw, cw, text) => {
    const item = { text: text || label(el), sel: sel(el), sw, cw }
    const key = `${item.sel}|${item.text}|${sw}|${cw}`
    if (!seenClip.has(key)) {
      seenClip.add(key)
      clipped.push(item)
    }
  }
  const clips = (cs) => /(hidden|clip)/.test(cs.overflowX)
  for (const el of document.querySelectorAll('body *')) {
    if (!visible(el) || el.closest('pre, svg, input, textarea, select')) continue
    const cs = getComputedStyle(el)
    const rect = el.getBoundingClientRect()
    const hasText = el.innerText && el.innerText.trim()
    const ownText = [...el.childNodes].some((c) => c.nodeType === 3 && c.textContent.trim())
    const wide = el.scrollWidth > el.clientWidth + 1
    if (ownText && wide && !/(auto|scroll)/.test(cs.overflowX) && cs.textOverflow !== 'ellipsis') pushClip(el, el.scrollWidth, el.clientWidth)
    else if (hasText && wide && clips(cs) && cs.textOverflow !== 'ellipsis' && rect.width >= 8 && rect.height >= 8) pushClip(el, el.scrollWidth, el.clientWidth)
    if (ownText && el.clientWidth === 0) {
      for (const n of el.childNodes) {
        if (n.nodeType !== 3 || !n.textContent.trim()) continue
        const range = document.createRange()
        range.selectNodeContents(n)
        const tr = range.getBoundingClientRect()
        if (tr.width === 0) continue
        for (let c = el.parentElement; c && c !== document.documentElement; c = c.parentElement) {
          const ccs = getComputedStyle(c)
          if (ccs.overflowX === 'visible') continue
          // Первый предок с любым не-visible overflow-x решает: прокручиваемый
          // (auto/scroll) -- текст уехал, а не обрезан (чип полосы за краем), и
          // выше идти нельзя -- там уже не его обрезка.
          if (clips(ccs) && ccs.textOverflow !== 'ellipsis') {
            const cr = c.getBoundingClientRect()
            const left = cr.left + c.clientLeft
            const right = left + c.clientWidth
            if (tr.right > right + 1 || tr.left < left - 1) pushClip(el, Math.round(tr.width), c.clientWidth, n.textContent.trim().replace(/\s+/g, ' ').slice(0, 60))
          }
          break
        }
      }
    }
  }

  // Ряд -- соседние видимые кнопки (и a.btn) одного предка на одной полосе по
  // вертикали (перекрытие, а не равный top). Предок -- ближайший без
  // display:contents: у «Перезапустить / Не беспокоить» обёртка прозрачна для
  // раскладки, и кнопки -- соседи по ряду родителя.
  const rows = []
  const layoutParent = (el) => {
    let p = el.parentElement
    while (p && getComputedStyle(p).display === 'contents') p = p.parentElement
    return p
  }
  const groups = new Map()
  for (const b of document.querySelectorAll('button, a.btn')) {
    if (!visible(b)) continue
    const p = layoutParent(b)
    if (!p) continue
    if (!groups.has(p)) groups.set(p, [])
    groups.get(p).push(b)
  }
  for (const [parent, kids] of groups) {
    if (kids.length < 2) continue
    const lines = []
    for (const k of kids) {
      const r = k.getBoundingClientRect()
      const item = { text: label(k), h: Math.round(r.height) }
      const line = lines.find((l) => r.top < l.bottom - 1 && r.bottom > l.top + 1)
      if (line) {
        line.items.push(item)
        line.top = Math.min(line.top, r.top)
        line.bottom = Math.max(line.bottom, r.bottom)
      } else lines.push({ top: r.top, bottom: r.bottom, items: [item] })
    }
    for (const l of lines) if (l.items.length > 1) rows.push({ sel: sel(parent), items: l.items })
  }

  // Элементы управления в оформлении браузера (проверка 8). Что именно рисует
  // браузер, меряется тут же: пробный элемент с all:revert -- без правил автора.
  const CONTROLS = 'button, a.btn, input:not([type=checkbox]):not([type=radio]):not([type=hidden]):not([type=range]):not([type=file]), select, textarea, summary'
  const ua = {}
  for (const tag of ['button', 'input', 'select', 'textarea', 'a']) {
    const probe = document.createElement(tag)
    if (tag === 'a') probe.href = '#layout-probe'
    probe.style.cssText = 'all: revert; position: fixed; left: -9999px; top: 0'
    document.body.appendChild(probe)
    const pcs = getComputedStyle(probe)
    ua[tag] = { bg: pcs.backgroundColor, font: pcs.fontFamily, color: pcs.color }
    probe.remove()
  }
  const rootCS = getComputedStyle(document.documentElement)
  const fonts = [getComputedStyle(document.body).fontFamily, rootCS.getPropertyValue('--font-body'), rootCS.getPropertyValue('--font-mono')].map((f) => f.trim()).filter(Boolean)
  const controls = [...document.querySelectorAll(CONTROLS)].filter(visible).map((el) => {
    const cs = getComputedStyle(el)
    return { tag: el.tagName.toLowerCase(), text: label(el), sel: sel(el), bg: cs.backgroundColor, border: cs.borderTopStyle, font: cs.fontFamily, color: cs.color }
  })

  // Полоса «Мои роутеры» (проверка 9): видимая часть каждого чипа -- его
  // прямоугольник, обрезанный ближайшим предком с прокруткой или обрезкой
  // внутри полосы. hit -- что получит палец в середине видимой части.
  const strip = []
  const stripEl = document.querySelector('.router-strip')
  if (stripEl && visible(stripEl)) {
    const clipBox = (el) => {
      for (let p = el.parentElement; p; p = p.parentElement) {
        if (getComputedStyle(p).overflowX !== 'visible') return p.getBoundingClientRect()
        if (p === stripEl) break
      }
      return null
    }
    for (const chip of stripEl.querySelectorAll('.strip-chip')) {
      const r = chip.getBoundingClientRect()
      const box = clipBox(chip)
      const left = box ? Math.max(r.left, box.left) : r.left
      const right = box ? Math.min(r.right, box.right) : r.right
      if (right - left <= 0 || r.height === 0) continue
      const x = (left + right) / 2
      const y = (r.top + r.bottom) / 2
      const inView = x >= 0 && x < window.innerWidth && y >= 0 && y < window.innerHeight
      strip.push({ text: label(chip), alert: chip.classList.contains('strip-chip-alert'), left, right, top: r.top, bottom: r.bottom, hit: inView ? chip.contains(document.elementFromPoint(x, y)) : true })
    }
  }

  // Карточки одного ряда сетки (проверка 10): у карточек с рядом кнопок этот
  // ряд обязан стоять на одной высоте -- иначе кнопки соседей «пляшут».
  const gridCards = []
  for (const grid of document.querySelectorAll('body *')) {
    if (!visible(grid) || !/grid/.test(getComputedStyle(grid).display)) continue
    // Карточка без своей коробки (display: contents -- её части сами стоят в
    // сетке, Парк v0.52.1) меряется по первой части.
    const boxOf = (c) => (getComputedStyle(c).display === 'contents' ? c.firstElementChild ?? c : c)
    const cards = [...grid.children].filter((c) => c.classList.contains('card') && visible(boxOf(c)))
    if (cards.length < 2) continue
    for (const c of cards) {
      const btn = [...c.querySelectorAll('.action-row button, .action-row a.btn')].find(visible)
      if (!btn) continue
      gridCards.push({ sel: sel(grid), name: (boxOf(c).innerText || '').trim().split('\n')[0].trim().slice(0, 40), top: boxOf(c).getBoundingClientRect().top, btnTop: btn.getBoundingClientRect().top })
    }
  }

  // Уровни заголовков (проверка 11): у заголовка внутри раздела/свёртки, чей
  // собственный заголовок -- другой, уровень обязан быть глубже.
  const headings = []
  const H = 'h1, h2, h3, h4, h5, h6'
  for (const h of document.querySelectorAll(H)) {
    if (!visible(h)) continue
    let owner = null
    for (let box = h.parentElement?.closest('section, details'); box && !owner; box = box.parentElement?.closest('section, details')) {
      const own = box.querySelector(H)
      if (own && own !== h && visible(own)) owner = { level: Number(own.tagName[1]), text: label(own) }
    }
    headings.push({ level: Number(h.tagName[1]), text: label(h), owner })
  }

  return { scrollWidth: document.scrollingElement.scrollWidth, innerWidth: window.innerWidth, targets, limes, smallText, clipped, rows, controls, fonts, ua, strip, gridCards, headings }
}

// Проверка 9: видимые части двух чипов полосы не пересекаются (допуск 0,5 px),
// и касание в середину видимой части чипа попадает в него самого.
export function stripProblems(strip = []) {
  const out = []
  for (let i = 0; i < strip.length; i++) {
    for (let j = i + 1; j < strip.length; j++) {
      const a = strip[i]
      const b = strip[j]
      const w = Math.min(a.right, b.right) - Math.max(a.left, b.left)
      const h = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
      if (w > 0.5 && h > 0.5) out.push(`чипы полосы перекрываются на ${Math.round(w)} px: «${a.text}» ${Math.round(a.left)}–${Math.round(a.right)} и «${b.text}» ${Math.round(b.left)}–${Math.round(b.right)}`)
    }
  }
  for (const c of strip) if (c.hit === false) out.push(`касание чипа «${c.text}» попадает в другой элемент`)
  return out
}

// Проверка 11: заголовок внутри раздела с собственным заголовком того же или
// более глубокого уровня -- у скринридера «h2 в h2».
export function headingProblems(headings = []) {
  return headings.filter((h) => h.owner && h.level <= h.owner.level).map((h) => `заголовок h${h.level} «${h.text}» вложен в раздел с заголовком h${h.owner.level} «${h.owner.text}»`)
}

// Раскрытая карточка ряда (v0.52.1): сосед не должен ни вытягиваться, ни
// двигать свои кнопки. before/after -- замеры соседа до и после раскрытия
// { name, height, btnTop }; допуск 1 px.
export function neighbourProblems(opened, before, after) {
  const out = []
  if (!before || !after) return [`сосед карточки «${opened}» не найден`]
  if (Math.abs(after.height - before.height) > 1) out.push(`раскрыли «${opened}» -- сосед «${before.name}» сменил высоту: ${Math.round(before.height)} → ${Math.round(after.height)}`)
  if (Math.abs(after.btnTop - before.btnTop) > 1) out.push(`раскрыли «${opened}» -- кнопки соседа «${before.name}» уехали: ${Math.round(before.btnTop)} → ${Math.round(after.btnTop)}`)
  return out
}

// Сверка соседа обязана состояться: от 1100 px карточки Парка стоят по две, и
// при двух карточках и больше ноль сверенных пар -- не «всё хорошо», а сверка,
// прошедшая вхолостую (ряды не нашлись). Уже 1100 px -- столбик, пар нет.
export const PAIR_MIN_WIDTH = 1100
export function pairCoverageProblem(width, cards, compared) {
  if (width >= PAIR_MIN_WIDTH && cards >= 2 && compared === 0) return `на ширине ${width} у ${cards} карточек не найдено ни одного ряда из двух -- сосед не сверен`
  return null
}

// Проверка 10: карточки одной сетки с одинаковым верхом -- один ряд; верх
// первой кнопки у них обязан совпадать (допуск 1 px).
export function gridRowProblems(cards = []) {
  const out = []
  const rows = new Map()
  for (const c of cards) {
    const key = `${c.sel}|${Math.round(c.top)}`
    if (!rows.has(key)) rows.set(key, [])
    rows.get(key).push(c)
  }
  for (const row of rows.values()) {
    const tops = row.map((c) => c.btnTop)
    if (row.length > 1 && Math.max(...tops) - Math.min(...tops) > 1) {
      out.push(`кнопки карточек одного ряда на разной высоте: ${row.map((c) => `«${c.name}» ${Math.round(c.btnTop)}`).join(', ')} (${row[0].sel})`)
    }
  }
  return out
}

// Проверка 8: кнопка, поле, список или раскрывашка в оформлении браузера --
// фон «лица кнопки» (серый 239/240, либо измеренный на этой странице у пробного
// элемента того же тега), рамка outset/inset, шрифт не из стека приложения,
// у ссылки-кнопки -- цвет ссылки браузера. Чекбоксы и радио сюда не входят:
// их рисует система по замыслу (accent-color), размер сторожит проверка 2.
const UA_FACES = ['rgb(239, 239, 239)', 'rgb(240, 240, 240)', 'buttonface']
const firstFamily = (f) => (f || '').split(',')[0].trim().replace(/^["']|["']$/g, '').toLowerCase()

export function unstyledControls({ controls = [], fonts = [], ua = {} } = {}) {
  const app = new Set(fonts.map(firstFamily).filter(Boolean))
  const out = []
  for (const c of controls) {
    const why = []
    const face = ua[c.tag]?.bg
    if ((c.tag === 'button' && UA_FACES.includes(c.bg)) || (face && face !== 'rgba(0, 0, 0, 0)' && c.bg === face)) why.push(`фон браузера ${c.bg}`)
    if (c.border === 'outset' || c.border === 'inset') why.push(`рамка ${c.border}`)
    if (app.size && !app.has(firstFamily(c.font))) why.push(`шрифт не приложения: ${c.font}`)
    if (c.tag === 'a' && ua.a?.color && c.color === ua.a.color) why.push(`цвет ссылки браузера ${c.color}`)
    if (why.length) out.push({ ...c, why })
  }
  return out
}

export function rowMismatches(rows = []) {
  return rows.filter((r) => {
    const hs = r.items.map((i) => i.h)
    return Math.max(...hs) - Math.min(...hs) > 1
  })
}

export function findProblems(d) {
  const out = []
  if (d.scrollWidth > d.innerWidth) out.push({ check: 1, what: `страница шире окна: ${d.scrollWidth} > ${d.innerWidth}` })
  for (const t of d.targets) if (t.w < 40 || t.h < 40) out.push({ check: 2, what: `цель меньше 40×40: «${t.text}» ${t.w}×${t.h} (${t.sel})` })
  if (d.limes.length > 1) out.push({ check: 3, what: `лаймовых кнопок ${d.limes.length}: ${d.limes.join(' | ')}` })
  for (const s of d.smallText) out.push({ check: 4, what: `текст ${s.size}px < 12: «${s.text}» (${s.sel})` })
  for (const c of d.clipped) out.push({ check: 5, what: `текст обрезан: «${c.text}» ${c.sw} > ${c.cw} (${c.sel})` })
  for (const r of rowMismatches(d.rows)) out.push({ check: 6, what: `кнопки одного ряда разной высоты: ${r.items.map((i) => `«${i.text}» ${i.h}`).join(', ')} (${r.sel})` })
  for (const c of unstyledControls(d)) out.push({ check: 8, what: `элемент в оформлении браузера: «${c.text}» -- ${c.why.join('; ')} (${c.sel})` })
  for (const w of stripProblems(d.strip)) out.push({ check: 9, what: w })
  for (const w of gridRowProblems(d.gridCards)) out.push({ check: 10, what: w })
  for (const w of headingProblems(d.headings)) out.push({ check: 11, what: w })
  return out
}

// Известные ответы фейкового агента песочницы: запись -- только с причиной,
// почему это ожидаемо (why). Пустой список -- любой ответ ≥ 400 находка.
export const KNOWN_NOISE = [
  // Панель «Старый пароль» заперта по замыслу сида (LockBadPassword): список
  // пиров отвечает 409, экран показывает баннер.
  { kind: 'http', status: 409, method: 'GET', url: /^\/v1\/miniapp\/awg3panels\/old\/peers$/, why: 'заперта по замыслу сида (неверный пароль): 409 -- ожидаемый ответ, экран рисует баннер' },
  // Панель «Бан 15 минут» на паузе по замыслу сида (PausedUntil): пиры -- 409.
  { kind: 'http', status: 409, method: 'GET', url: /^\/v1\/miniapp\/awg3panels\/ban\/peers$/, why: 'на паузе по замыслу сида (бан 15 минут): 409 awg3_paused -- ожидаемый ответ, экран рисует баннер' },
]

export function isKnownNoise(e) {
  return KNOWN_NOISE.some((n) => e.kind === n.kind && n.status === e.status && n.method === e.method && n.url.test(e.url))
}

// Строка консоли «Failed to load resource ... status NNN» -- дубль http-события
// браузером; отбрасывается, только если в тех же событиях экрана есть ответ с
// этим кодом (сам ответ оценивается отдельно: находка остаётся находкой).
const RESOURCE_DUP = /^Failed to load resource: the server responded with a status of (\d{3})/

export function netProblems(events = []) {
  const seen = new Set(events.filter((e) => e.kind === 'http').map((e) => String(e.status)))
  return events
    .filter((e) => !(e.kind === 'console' && RESOURCE_DUP.test(e.text) && seen.has(e.text.match(RESOURCE_DUP)[1])))
    .filter((e) => !isKnownNoise(e))
    .map((e) => ({
      check: 7,
      what: e.kind === 'http' ? `ответ ${e.status}: ${e.method} ${e.url}${e.detail ? ` (${e.detail})` : ''}` : `консоль: ${e.text}`,
    }))
}
