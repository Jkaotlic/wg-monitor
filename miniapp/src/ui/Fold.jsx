import { useEffect, useRef } from 'preact/hooks'

// Свёртка (v0.50): заголовок и итоговая строка видны всегда, тело -- по
// нажатию. Нативный <details>: клавиатура, скринридер и поиск по странице
// работают сами, а содержимое остаётся в DOM и в свёрнутом виде. open --
// управляемое снаружи, когда задано (фокус группы по старой ссылке);
// onToggle сообщает решение человека.
export function Fold({ id, title, note, open, onToggle, class: cls = '', titleTag = 'span', titleClass = '', noteTone = '', children }) {
  const ref = useRef(null)
  useEffect(() => {
    if (ref.current && typeof open === 'boolean' && ref.current.open !== open) ref.current.open = open
  }, [open])
  const Title = titleTag
  return (
    <details id={id} ref={ref} class={`fold ${cls}`.trim()} open={open === true ? true : undefined} onToggle={(e) => onToggle?.(e.currentTarget.open)}>
      <summary class="fold-summary">
        <Title class={`fold-title ${titleClass}`.trim()}>{title}</Title>
        {note ? <span class={noteTone ? `fold-note fold-note-${noteTone}` : 'fold-note'}>{note}</span> : null}
      </summary>
      <div class="fold-body">{children}</div>
    </details>
  )
}
