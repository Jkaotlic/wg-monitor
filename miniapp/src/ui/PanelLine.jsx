import { openExternal } from '../telegram.js'
import { panelHost, panelLink } from '../settings.js'

// Адрес панели awg-manager строкой под именем роутера (v0.41, просьба
// оператора): хост, который человек узнаёт, и нажатие открывает панель во
// внешнем браузере. Сервер отдаёт адрес только владельцу и админу -- нет
// адреса, нет и строки.
//
// nested -- строка стоит внутри строки-кнопки (список роутеров, боковая
// колонка). Ссылку в кнопку не вкладываем: там хост -- просто текст, а
// нажатие открывает роутер, как и вся строка. Ссылкой на панель адрес
// служит в шапке роутера и первым пунктом «Управления».
export function PanelLine({ url, nested = false }) {
  const link = panelLink(url)
  if (!link) return null
  const host = panelHost(link)
  if (nested) return <span class="panel-line panel-line-text">{host}</span>
  const open = (e) => {
    e.stopPropagation()
    e.preventDefault()
    openExternal(link)
  }
  const title = `Открыть панель awg-manager: ${host}`
  return (
    <button type="button" class="panel-line" title={title} onClick={open}>
      {host}
    </button>
  )
}
