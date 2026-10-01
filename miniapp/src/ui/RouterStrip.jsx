import { useEffect, useRef } from 'preact/hooks'

// Полоса «Мои роутеры» (v0.52, спека §4): 2–5 роутеров -- чипы «имя + точка»
// под шапкой на всех вкладках роутера. Тревога первой и окрашена; полоса
// прокручивается сама, страница -- нет. Состояние словами -- в aria-label.
//
// Текущий чип всегда в кадре: тревожный стоит первым, и на 360 px третий чип
// иначе оставался бы за краем -- человек не видел бы, на каком он роутере.
// Перематывать надо и когда чипы переставились (тревога сменила порядок), а не
// только когда сменился текущий или их число: ключ -- сам порядок.
//
// Первый тревожный чип прилипает к левому краю полосы (style.css): прокрутив
// вправо, человек не теряет из виду, что где-то красно. Прилипший чип
// закрывает кусок кадра -- перемотка его учитывает.
export function RouterStrip({ chips = [], onPick }) {
  const strip = useRef(null)
  const currentID = chips.find((c) => c.current)?.id
  const order = chips.map((c) => c.id).join(',')
  useEffect(() => {
    const el = strip.current
    const chip = el?.querySelector('.strip-chip-current')
    if (!el || !chip) return
    // Только сама полоса: scrollIntoView двигал бы и страницу.
    const pad = 16
    const left = chip.offsetLeft - el.offsetLeft
    const lead = el.querySelector('.strip-chip-alert:first-child')
    const inset = lead && lead !== chip ? lead.offsetWidth + 8 : 0
    if (left - pad - inset < el.scrollLeft) el.scrollLeft = Math.max(0, left - pad - inset)
    else if (left + chip.offsetWidth + pad > el.scrollLeft + el.clientWidth) el.scrollLeft = left + chip.offsetWidth + pad - el.clientWidth
  }, [currentID, order])
  return (
    <nav ref={strip} class="router-strip" aria-label="Мои роутеры">
      {chips.map((c) => (
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
      ))}
    </nav>
  )
}
