// Где строке владельца нельзя переноситься.
//
// Браузер рвёт строку после дефиса, и узкий экран телефона получает «через
// VPN-» / «туннель «vpn-» / «nl»». Термин «VPN-туннель» и имя в «ёлочках» --
// по одному слову для глаза: разорванное, оно читается как два. Неразрывный
// дефис U+2011 выглядит как обычный, но переноса по нему нет.
//
// Строк с «VPN-туннелем» в мини-аппе под двести, в тридцати файлах, и каждая
// новая -- ещё одна. Чинить их поштучно значит пропустить следующую, поэтому
// склейка ставится один раз на весь рендер (installKeepTogether в main.jsx):
// хук Preact options.vnode видит каждый текстовый узел до того, как тот
// попадёт в DOM. Атрибуты -- value, placeholder, aria-label -- он не трогает:
// там переносов нет, а введённое человеком менять нельзя.

const NB_HYPHEN = '‑'
const TERM = /(VPN)-(туннел)/gi
// Имя в «ёлочках» -- без вложенных кавычек и переводов строки: незакрытая
// кавычка не должна склеить остаток абзаца.
const QUOTED = /«[^«»\n]*»/g

export function keepTogether(text) {
  if (typeof text !== 'string' || !text.includes('-')) return text
  return text
    .replace(TERM, `$1${NB_HYPHEN}$2`)
    .replace(QUOTED, (quoted) => quoted.replaceAll('-', NB_HYPHEN))
}

// Текстовый узел Preact -- vnode с type == null и строкой в props. Прежний
// хук (devtools, hooks) вызывается после, уже со склеенным текстом.
export function installKeepTogether(options) {
  const prev = options.vnode
  options.vnode = (vnode) => {
    if (vnode.type == null && typeof vnode.props === 'string') {
      vnode.props = keepTogether(vnode.props)
    }
    if (prev) prev(vnode)
  }
}
