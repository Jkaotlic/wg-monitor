// Строка списка: заголовок, подпись под ним, необязательный правый слот и
// шеврон у кликабельных строк.
export function ListRow({ title, sub, right, onClick, tone }) {
  const clickable = typeof onClick === 'function'
  return (
    <li
      class={`row list-row${clickable ? ' row-clickable' : ''}${tone ? ` list-row-${tone}` : ''}`}
      onClick={onClick}
    >
      <span class="list-row-main">
        <span class="row-title">{title}</span>
        {sub && <span class="list-row-sub">{sub}</span>}
      </span>
      {right}
      {clickable && (
        <svg class="list-row-chevron" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M 6 3 L 11 8 L 6 13" />
        </svg>
      )}
    </li>
  )
}
