import { openExternal } from '../telegram.js'
import { panelHost, panelLink } from '../settings.js'

// Адрес панели awg-manager строкой под именем роутера (v0.41, просьба
// оператора): хост, который человек узнаёт, и нажатие открывает панель во
// внешнем браузере. Сервер отдаёт адрес только владельцу и админу -- нет
// адреса, нет и строки.
//
// nested -- строка стоит внутри строки-кнопки (список роутеров, боковая
// колонка). Кнопку в кнопку вкладывать нельзя, поэтому там это span с ролью
// ссылки, и нажатие не всплывает до строки: иначе вместо панели открылся бы
// роутер.
export function PanelLine({ url, nested = false }) {
  const link = panelLink(url)
  if (!link) return null
  const host = panelHost(link)
  const open = (e) => {
    e.stopPropagation()
    e.preventDefault()
    openExternal(link)
  }
  const title = `Открыть панель awg-manager: ${host}`
  if (nested) {
    return (
      <span
        role="link"
        tabIndex={0}
        class="panel-line"
        title={title}
        onClick={open}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') open(e)
        }}
      >
        {host}
      </span>
    )
  }
  return (
    <button type="button" class="panel-line" title={title} onClick={open}>
      {host}
    </button>
  )
}
