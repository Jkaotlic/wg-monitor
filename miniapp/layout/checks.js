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
  const overlays = [...document.querySelectorAll('.overlay')]
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

  const targets = [...document.querySelectorAll('button, a[href], [role=button], [role=tab], summary, .strip-chip')]
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

  // Обрезан -- элемент со своим текстом, чей текст шире его самого, без своей
  // прокрутки и без многоточия по замыслу.
  const clipped = []
  for (const el of document.querySelectorAll('body *')) {
    if (!visible(el) || el.closest('pre, svg, input, textarea, select')) continue
    const ownText = [...el.childNodes].some((c) => c.nodeType === 3 && c.textContent.trim())
    if (!ownText || el.scrollWidth <= el.clientWidth + 1) continue
    const cs = getComputedStyle(el)
    if (/(auto|scroll)/.test(cs.overflowX) || cs.textOverflow === 'ellipsis') continue
    clipped.push({ text: label(el), sel: sel(el), sw: el.scrollWidth, cw: el.clientWidth })
  }

  // Ряд -- соседние видимые кнопки одного родителя на одной высоте (±2 px).
  const rows = []
  const parents = new Set([...document.querySelectorAll('button')].filter(visible).map((b) => b.parentElement))
  for (const parent of parents) {
    const kids = [...parent.children].filter((c) => c.tagName === 'BUTTON' && visible(c))
    if (kids.length < 2) continue
    const lines = []
    for (const k of kids) {
      const r = k.getBoundingClientRect()
      const line = lines.find((l) => Math.abs(l.top - r.top) <= 2)
      const item = { text: label(k), h: Math.round(r.height) }
      if (line) line.items.push(item)
      else lines.push({ top: r.top, items: [item] })
    }
    for (const l of lines) if (l.items.length > 1) rows.push({ sel: sel(parent), items: l.items })
  }

  return { scrollWidth: document.scrollingElement.scrollWidth, innerWidth: window.innerWidth, targets, limes, smallText, clipped, rows }
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
  return out
}

// Известные ответы фейкового агента песочницы: запись -- только с причиной,
// почему это ожидаемо (why). Пустой список -- любой ответ ≥ 400 находка.
export const KNOWN_NOISE = [
  // Фейковый агент sandbox-work не знает туннелей, что рисует seed (awg12):
  // трафик по ним честно отвечает unknown_tunnel. Экран это переживает.
  { kind: 'http', status: 400, method: 'POST', url: /^\/v1\/miniapp\/routers\/\d+\/commands$/, why: 'tunnel_traffic: фейковый агент не знает туннель awg12 (unknown_tunnel)' },
  // Панель «Старый пароль» заперта по замыслу сида (LockBadPassword): список
  // пиров отвечает 409, экран показывает баннер.
  { kind: 'http', status: 409, method: 'GET', url: /^\/v1\/miniapp\/awg3panels\/old\/peers$/, why: 'заперта по замыслу сида (неверный пароль): 409 -- ожидаемый ответ, экран рисует баннер' },
  // Браузер дублирует каждый http >= 400 строкой консоли без адреса; сам ответ
  // оценивается отдельно (строки выше), поэтому дубль не считается.
  { kind: 'console', text: /^Failed to load resource: the server responded with a status of \d{3}/, why: 'дубль http-события консолью браузера; ответ оценивается отдельно' },
]

export function isKnownNoise(e) {
  return KNOWN_NOISE.some((n) =>
    e.kind === n.kind && (n.kind === 'console' ? n.text.test(e.text) : n.status === e.status && n.method === e.method && n.url.test(e.url)),
  )
}

export function netProblems(events = []) {
  return events
    .filter((e) => !isKnownNoise(e))
    .map((e) => ({ check: 7, what: e.kind === 'http' ? `ответ ${e.status}: ${e.method} ${e.url}` : `консоль: ${e.text}` }))
}
