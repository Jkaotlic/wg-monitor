import { useEffect, useRef } from 'preact/hooks'

// Полоса «Мои роутеры» (v0.52, спека §4): 2–5 роутеров -- чипы «имя + точка»
// под шапкой на всех вкладках роутера. Тревога первой и окрашена; полоса
// прокручивается сама, страница -- нет. Состояние словами -- в aria-label.
//
// Первый тревожный чип стоит ВНЕ прокрутки -- отдельной ячейкой слева
// (.strip-lead), остальные ездят в своей прокрутке рядом (.strip-scroll).
// Прежде он прилипал внутри прокрутки (position: sticky), и прокрученные рукой
// чипы уезжали под него -- палец попадал в красный вместо нужного. Теперь
// уезжать не подо что: чипы обрезаются краем своей прокрутки.
//
// Текущий чип всегда в кадре своей прокрутки: на 360 px третий чип иначе
// оставался бы за краем -- человек не видел бы, на каком он роутере.
// Перематывать надо и когда чипы переставились (тревога сменила порядок), а не
// только когда сменился текущий или их число: ключ -- сам порядок.
const PAD = 16

export function RouterStrip({ chips = [], onPick }) {
  const scroll = useRef(null)
  const currentID = chips.find((c) => c.current)?.id
  const order = chips.map((c) => c.id).join(',')
  const lead = chips[0]?.alert ? chips[0] : null
  const rest = lead ? chips.slice(1) : chips
  useEffect(() => {
    const el = scroll.current
    // Текущий -- сам красный: он в своей ячейке и виден всегда.
    const chip = el?.querySelector('.strip-chip-current')
    if (!el || !chip) return
    // Только сама прокрутка: scrollIntoView двигал бы и страницу. Прокрутка --
    // offsetParent своих чипов (position: relative), offsetLeft от неё.
    const left = chip.offsetLeft
    // Левое поле прокрутки -- по первому чипу: рядом с красным оно уже.
    const padLeft = el.firstElementChild?.offsetLeft ?? 0
    // hi -- самая правая прокрутка, при которой начало текущего ещё в кадре;
    // lo -- самая левая, при которой он виден до правого края.
    const hi = left - padLeft
    const lo = left + chip.offsetWidth + PAD - el.clientWidth
    let next = el.scrollLeft
    if (next > hi) next = hi
    else if (next < lo) next = Math.min(lo, hi)
    // Оба условия вместе не выполнить (длинное имя) -- побеждает начало имени.
    el.scrollLeft = Math.max(0, next)
  }, [currentID, order])
  const chip = (c) => (
    <button
      key={c.id}
      type="button"
      class={`strip-chip${c.current ? ' strip-chip-current' : ''}${c.alert ? ' strip-chip-alert' : ''}`}
      aria-current={c.current ? 'page' : undefined}
      aria-label={`${c.name}: ${c.state}`}
      onClick={() => !c.current && onPick(c.id)}
    >
      <span class={`strip-dot strip-dot-${c.tone}`} aria-hidden="true" />
      <span class="strip-name">{c.name}</span>
    </button>
  )
  return (
    <nav class="router-strip" aria-label="Мои роутеры">
      {lead && <div class="strip-lead">{chip(lead)}</div>}
      <div ref={scroll} class="strip-scroll">
        {rest.map(chip)}
      </div>
    </nav>
  )
}
