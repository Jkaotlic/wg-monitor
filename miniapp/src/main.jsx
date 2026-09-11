import { render, options } from 'preact'
import { App } from './App.jsx'
import { installKeepTogether } from './text.js'
import './style.css'

// «VPN-туннель» и имена в «ёлочках» не рвутся переносом ни на одном экране:
// склейка стоит на рендере, а не в каждой строке (см. text.js).
installKeepTogether(options)

render(<App />, document.getElementById('app'))
