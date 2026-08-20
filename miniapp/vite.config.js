import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'
import { respond, setLastAction } from './dev/fixtures.js'

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
        if (req.method === 'POST' && path.endsWith('/commands')) {
          // Запоминаем действие, чтобы отдать правдоподобный именно для него
          // результат при опросе.
          let raw = ''
          req.on('data', (chunk) => { raw += chunk })
          req.on('end', () => {
            try {
              setLastAction(JSON.parse(raw))
            } catch {
              setLastAction(null)
            }
            res.setHeader('Content-Type', 'application/json')
            res.end(JSON.stringify({ cmd_id: 'dev' }))
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
