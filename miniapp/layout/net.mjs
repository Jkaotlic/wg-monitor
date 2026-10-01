// Сбор консоли и ответов >= 400 страницы -- общий для run.mjs и selftest.mjs,
// чтобы самопроверка гоняла ровно тот код, что и приёмка.
export function watchNet(page, sink) {
  page.on('console', (m) => {
    if (m.type() === 'error') sink.push({ kind: 'console', text: m.text() })
  })
  page.on('pageerror', (e) => sink.push({ kind: 'console', text: e.message }))
  page.on('response', async (r) => {
    if (r.status() < 400) return
    const req = r.request()
    const event = { kind: 'http', status: r.status(), method: req.method(), url: new URL(r.url()).pathname }
    // Действие команды и код ответа -- чтобы находка читалась без лога песочницы.
    try {
      const body = req.postDataJSON()
      const code = (await r.json())?.code
      event.detail = [body?.action, body?.args?.tunnel_id, code].filter(Boolean).join(' ')
    } catch {
      // тело не JSON -- деталей нет
    }
    sink.push(event)
  })
}
