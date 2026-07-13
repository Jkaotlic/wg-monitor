const BASE = '/v1/miniapp'

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function request(path, opts = {}) {
  const res = await fetch(BASE + path, {
    ...opts,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers ?? {}) },
  })
  if (!res.ok) {
    let code = 'unknown'
    try {
      const body = await res.json()
      code = body.error ?? code
    } catch {
      // ignore non-JSON error bodies
    }
    throw new ApiError(res.status, code, `${path} failed: ${res.status}`)
  }
  return res.json()
}

export function createSession(initData) {
  return request('/session', { method: 'POST', body: JSON.stringify({ init_data: initData }) })
}

export function fetchRouters() {
  return request('/routers')
}

export function fetchRouter(id) {
  return request(`/routers/${id}`)
}

export function fetchRouterChecks(id) {
  return request(`/routers/${id}/events`)
}
