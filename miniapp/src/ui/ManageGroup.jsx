import { toChildArray } from 'preact'

// Группа разделов вкладки «Управление» (v0.47): подзаголовок над несколькими
// Section. Пустая группа -- ни одного видимого раздела по правам роли -- не
// рисуется вовсе: заголовок «Починить» без единой кнопки под ним выглядел бы
// как поломка. Скрытые разделы приходят сюда как false/null, их toChildArray
// и отбрасывает.
export function ManageGroup({ title, children }) {
  if (toChildArray(children).length === 0) return null
  return (
    <div class="manage-group">
      <h2 class="manage-group-title">{title}</h2>
      {children}
    </div>
  )
}
