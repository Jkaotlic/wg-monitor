import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchCabinets,
  addCabinetSecret,
  setCabinetActive,
  deleteCabinetSecret,
  revokeAmneziaSlot,
  issueVPNConfig,
  sendVPNConf,
  fetchSelfhosted,
  createSelfhosted,
  updateSelfhosted,
  toggleSelfhosted,
  deleteSelfhosted,
  checkSelfhosted,
  ApiError,
} from '../src/api.js'

function stubFetch(reply, { ok = true, status = 200 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts = {}) => {
    calls.push({
      url,
      method: opts.method ?? 'GET',
      headers: opts.headers ?? {},
      body: opts.body ? JSON.parse(opts.body) : undefined,
    })
    return {
      ok,
      status,
      json: async () => {
        if (status === 204) throw new Error('у 204 нет тела')
        return reply
      },
    }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('кабинеты роутера: запросы', () => {
  it('список -- GET /routers/{id}/cabinets', async () => {
    const calls = stubFetch({ amnezia: { keys: [] }, hidemy: { codes: [] }, selfhosted: { available: false } })
    await fetchCabinets(7)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/cabinets', method: 'GET' })
  })

  it('ключ Amnezia -- только в теле POST, в адресе его нет', async () => {
    const calls = stubFetch({ id: 'k1', label: 'основной', mask: '••••a1b2', active: true }, { status: 201 })
    expect(await addCabinetSecret(7, 'amnezia', 'vpn://SECRET', 'основной')).toEqual({ id: 'k1', label: 'основной', mask: '••••a1b2', active: true })
    expect(calls[0]).toMatchObject({
      url: '/v1/miniapp/routers/7/cabinets/amnezia/keys',
      method: 'POST',
      body: { vpn_key: 'vpn://SECRET', label: 'основной' },
    })
    expect(calls[0].url).not.toContain('SECRET')
    expect(calls[0].headers['Content-Type']).toBe('application/json')
  })

  it('код HideMy -- POST /cabinets/hidemy/codes с access_code', async () => {
    const calls = stubFetch({ id: 'c1', mask: '9876' }, { status: 201 })
    await addCabinetSecret(7, 'hidemy', '123456789', '')
    expect(calls[0]).toMatchObject({
      url: '/v1/miniapp/routers/7/cabinets/hidemy/codes',
      method: 'POST',
      body: { access_code: '123456789', label: '' },
    })
  })

  it('неизвестный кабинет -- исключение до запроса', () => {
    const calls = stubFetch({})
    expect(() => addCabinetSecret(7, 'selfhosted', 'x')).toThrow()
    expect(calls).toEqual([])
  })

  it('активный и удаление (204), id экранируется', async () => {
    let calls = stubFetch(null, { status: 204 })
    expect(await setCabinetActive(7, 'hidemy', 'c1')).toBe(null)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/cabinets/hidemy/active', method: 'PUT', body: { id: 'c1' } })

    calls = stubFetch(null, { status: 204 })
    expect(await deleteCabinetSecret(7, 'amnezia', 'k/1')).toBe(null)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/cabinets/amnezia/keys/k%2F1', method: 'DELETE' })

    calls = stubFetch(null, { status: 204 })
    await deleteCabinetSecret(7, 'hidemy', 'c1')
    expect(calls[0].url).toBe('/v1/miniapp/routers/7/cabinets/hidemy/codes/c1')
  })

  it('отзыв страны -- страна и набранное имя роутера', async () => {
    const calls = stubFetch({ country: 'nl' }, { status: 200 })
    await revokeAmneziaSlot(7, 'nl', 'dacha-1')
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/cabinets/amnezia/revoke', method: 'POST', body: { country: 'nl', confirm: 'dacha-1' } })
  })

  it('выпуск: без сервера -- тело прежнее; со своим сервером -- instance_id', async () => {
    let calls = stubFetch({ cmd_id: 'c1', tunnel_name: 'amnezia_nl' }, { status: 202 })
    await issueVPNConfig(7, 'amnezia', 'nl')
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/vpn/issue', method: 'POST', body: { provider: 'amnezia', option_id: 'nl' } })
    expect('instance_id' in calls[0].body).toBe(false)

    calls = stubFetch({ cmd_id: 'c2', tunnel_name: 'selfhosted_ams' }, { status: 202 })
    await issueVPNConfig(7, 'selfhosted', 'ams', 'ams')
    expect(calls[0].body).toEqual({ provider: 'selfhosted', option_id: 'ams', instance_id: 'ams' })
  })

  // Сверка с частью 1: выбор в send-conf -- option_id, как в vpn/issue.
  it('.conf в личку -- provider и option_id, instance_id только со своим сервером', async () => {
    let calls = stubFetch({ sent_to: 'dm' }, { status: 202 })
    expect(await sendVPNConf(7, { provider: 'hidemyname', option: 'srv-1' })).toEqual({ sent_to: 'dm' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/vpn/send-conf', method: 'POST', body: { provider: 'hidemyname', option_id: 'srv-1' } })
    expect('instance_id' in calls[0].body).toBe(false)
    expect('option' in calls[0].body).toBe(false)

    calls = stubFetch({ sent_to: 'dm' }, { status: 202 })
    await sendVPNConf(7, { provider: 'selfhosted', option: 'ams', instanceID: 'ams' })
    expect(calls[0].body).toEqual({ provider: 'selfhosted', option_id: 'ams', instance_id: 'ams' })
  })

  it('dm_unreachable -- ApiError с кодом и русской фразой', async () => {
    stubFetch({ code: 'dm_unreachable', message: 'Бот не может написать вам — нажмите /start' }, { ok: false, status: 409 })
    const err = await sendVPNConf(7, { provider: 'amnezia', option: 'nl' }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe('dm_unreachable')
    expect(err.serverMessage).toBe('Бот не может написать вам — нажмите /start')
  })
})

describe('свои серверы: запросы', () => {
  it('invalid_field -- ApiError несёт поле', async () => {
    stubFetch({ code: 'invalid_field', message: 'Порт вне диапазона', field: 'endpoint_port' }, { ok: false, status: 400 })
    const err = await createSelfhosted({ id: 'ams' }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.field).toBe('endpoint_port')
    expect(err.serverMessage).toBe('Порт вне диапазона')
    stubFetch({ code: 'not_found', message: 'x' }, { ok: false, status: 404 })
    expect((await fetchSelfhosted().catch((e) => e)).field).toBe('')
  })


  it('список, создание, правка', async () => {
    let calls = stubFetch({ instances: [] })
    await fetchSelfhosted()
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted', method: 'GET' })

    calls = stubFetch({ id: 'ams' }, { status: 201 })
    await createSelfhosted({ id: 'ams', ssh_password: 'pw' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted', method: 'POST', body: { id: 'ams', ssh_password: 'pw' } })

    calls = stubFetch(null, { status: 204 })
    expect(await updateSelfhosted('ams', { label: 'Амстердам' })).toBe(null)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted/ams', method: 'PUT', body: { label: 'Амстердам' } })
  })

  it('вкл/выкл, удаление с набором, проверка подключения', async () => {
    let calls = stubFetch(null, { status: 204 })
    await toggleSelfhosted('ams', false)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted/ams/toggle', method: 'POST', body: { enabled: false } })

    calls = stubFetch(null, { status: 204 })
    await deleteSelfhosted('a/b', 'Амстердам')
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted/a%2Fb', method: 'DELETE', body: { confirm: 'Амстердам' } })

    calls = stubFetch({ ok: false, message: 'SSH: неверный пароль' })
    expect(await checkSelfhosted('ams')).toEqual({ ok: false, message: 'SSH: неверный пароль' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/selfhosted/ams/check', method: 'POST' })
  })
})
