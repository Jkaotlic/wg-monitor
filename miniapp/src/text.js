import { Fragment } from 'preact'

// Где строке владельца нельзя переноситься -- и что при этом нельзя трогать.
//
// Браузер рвёт строку после дефиса: «через VPN-» / «туннель». Термин
// «VPN-туннель» -- одно слово для глаза, и его склеивает неразрывный дефис
// U+2011. Строк с термином в мини-аппе под двести, в тридцати файлах, поэтому
// склейка ставится один раз на весь рендер (installKeepTogether в main.jsx).
//
// Имена в «ёлочках» -- VPN-туннели, роутеры, сайты, правила -- символом не
// склеиваются никогда: их копируют и вставляют в роутер, в поиск, в поле
// подтверждения, и U+2011 там не совпадёт ни с чем. Имя держит вместе вёрстка
// -- <Q> (ui/Q.jsx), а внутрь «ёлочек» склейка не заходит. Дословный вывод
// агента (<pre>, .raw-dump) она не трогает вовсе.

const NB_HYPHEN = '‑'
// Имя в «ёлочках» совпадает целиком и возвращается как было; термин -- только
// вне кавычек. Имя -- без вложенных кавычек и переводов строки: незакрытая
// кавычка не должна проглотить остаток абзаца.
const TERM_OR_NAME = /«[^«»\n]*»|(VPN)-(туннел)/gi
const NAME = /«([^«»\n]*)»/g
const VERBATIM_CLASS = /(^|\s)(q|raw-dump)(\s|$)/

export function keepTogether(text) {
  if (typeof text !== 'string' || !text.includes('-')) return text
  return text.replace(TERM_OR_NAME, (m, vpn, tunnel) => (vpn ? `${vpn}${NB_HYPHEN}${tunnel}` : m))
}

// quoteParts режет готовую строку на текст и имена: '«a» через «b»' →
// [{ q: 'a' }, ' через ', { q: 'b' }]. Имена экран кладёт в <Q>.
export function quoteParts(text) {
  if (typeof text !== 'string' || text === '') return []
  const parts = []
  let last = 0
  for (const m of text.matchAll(NAME)) {
    if (m.index > last) parts.push(text.slice(last, m.index))
    parts.push({ q: m[1] })
    last = m.index + m[0].length
  }
  if (last < text.length) parts.push(text.slice(last))
  return parts
}

// plainHyphens -- обратная сторона склейки: скопированное из текста (U+2011,
// а откуда-то ещё и U+2010) во вводе значит обычный дефис.
export function plainHyphens(text) {
  return String(text ?? '').replace(/[‐‑]/g, '-')
}

function glue(children) {
  if (typeof children === 'string') return keepTogether(children)
  if (Array.isArray(children)) return children.map(glue)
  return children
}

function verbatim(type, props) {
  if (type === 'pre') return true
  const cls = props.class ?? props.className
  return typeof cls === 'string' && VERBATIM_CLASS.test(cls)
}

// Хук Preact options.vnode видит каждый vnode при создании. Склеиваются
// строки среди детей DOM-элемента или фрагмента -- там родитель известен, и
// дословное (<pre>, .raw-dump, имя в .q) пропускается целиком. Текстовые
// узлы, компоненты и атрибуты (value, placeholder) не трогаются: введённое
// человеком менять нельзя, а строки компонента дойдут до элемента сами.
// Прежний хук (devtools, hooks) вызывается после, уже со склеенным текстом.
export function installKeepTogether(options) {
  const prev = options.vnode
  options.vnode = (vnode) => {
    const { type, props } = vnode
    if ((typeof type === 'string' || type === Fragment) && props && props.children != null && !verbatim(type, props)) {
      props.children = glue(props.children)
    }
    if (prev) prev(vnode)
  }
}
