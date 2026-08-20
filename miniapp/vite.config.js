import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'
import { respond, registerCommand } from './dev/fixtures.js'

// Отдаёт заготовленные ответы вместо бэкенда -- только в режиме разработки.
// Нужен, чтобы открыть экраны в браузере (и снять с них скриншоты) без живого
// VPS и роутера. В сборку не попадает: configureServer работает лишь в dev.
function mockApi() {
  return {
    name: 'wg-monitor-mock-api',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url.startsWith('/v1/miniapp/')) return next()
        const path = req.url.split('?')[0]
        // Выпуск конфига из кабинета: сервер отвечает идентификатором
        // команды, конфиг клиенту не показывается вовсе.
        if (req.method === 'POST' && path.endsWith('/vpn/issue')) {
          let raw = ''
          req.on('data', (chunk) => { raw += chunk })
          req.on('end', () => {
            let option = 'nl'
            try {
              option = JSON.parse(raw).option_id ?? option
            } catch {
              option = 'nl'
            }
            const cmdID = registerCommand({ action: 'tunnel_import', args: { name: 'amnezia_' + option } })
            res.setHeader('Content-Type', 'application/json')
            res.statusCode = 202
            res.end(JSON.stringify({ cmd_id: cmdID, tunnel_name: 'amnezia_' + option }))
          })
          return
        }
        if (req.method === 'POST' && path.endsWith('/commands')) {
          // Запоминаем команду под её идентификатором, чтобы отдать при
          // опросе результат именно для неё: параллельные команды с одним
          // ответом на всех -- это фикстура, врущая экрану.
          let raw = ''
          req.on('data', (chunk) => { raw += chunk })
          req.on('end', () => {
            let cmdID = 'dev-unknown'
            try {
              cmdID = registerCommand(JSON.parse(raw))
            } catch {
              cmdID = registerCommand(null)
            }
            res.setHeader('Content-Type', 'application/json')
            res.end(JSON.stringify({ cmd_id: cmdID }))
          })
          return
        }
        const body = respond(req.method, path)
        if (body == null) {
          res.statusCode = 404
          res.setHeader('Content-Type', 'application/json')
          res.end(JSON.stringify({ code: 'not_found', message: 'нет фикстуры для ' + path }))
          return
        }
        res.setHeader('Content-Type', 'application/json')
        res.end(JSON.stringify(body))
      })
    },
  }
}

export default defineConfig({
  plugins: [preact(), mockApi()],
  base: '/miniapp/',
  build: {
    outDir: '../internal/backend/miniapp_static',
    emptyOutDir: true,
  },
})
