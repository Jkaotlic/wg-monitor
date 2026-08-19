// Строка списка: заголовок, подпись под ним, необязательный правый слот и
// шеврон у кликабельных строк.
//
// Кликабельная строка -- это кнопка внутри li, а не li с обработчиком:
// иначе строка недоступна с клавиатуры и невидима для скринридера, а
// мини-апп открывают и с десктопа тоже.
export function ListRow({ title, sub, right, onClick, tone }) {
  const clickable = typeof onClick === 'function'
  const content = (
    <>
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
    </>
  )

  if (!clickable) {
    return <li class={`row list-row${tone ? ` list-row-${tone}` : ''}`}>{content}</li>
  }
  return (
    <li class={`list-row-item${tone ? ` list-row-${tone}` : ''}`}>
      <button type="button" class="row list-row list-row-btn" onClick={onClick}>
        {content}
      </button>
    </li>
  )
}
