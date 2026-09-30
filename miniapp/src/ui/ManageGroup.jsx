import { toChildArray } from 'preact'
import { Fold } from './Fold.jsx'

// Группа вкладки «Управление» (v0.47; с v0.50 -- свёртка). Заголовок и
// итоговая строка видны всегда, разделы -- по нажатию или по чипу сверху.
// Пустая группа -- ни одного видимого раздела по правам роли -- не рисуется
// вовсе: заголовок «Починить» без единой кнопки под ним выглядел бы как
// поломка.
export function ManageGroup({ id, title, note, noteTone, open, onToggle, children }) {
  if (toChildArray(children).length === 0) return null
  return (
    <Fold id={id} class="manage-group" title={title} note={note} noteTone={noteTone} open={open} onToggle={onToggle} titleTag="h2" titleClass="manage-group-title">
      {children}
    </Fold>
  )
}
