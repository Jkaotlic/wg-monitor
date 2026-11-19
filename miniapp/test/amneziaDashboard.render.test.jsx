// @vitest-environment jsdom
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import { AmneziaDashboard } from '../src/screens/AmneziaDashboard.jsx'
import { openPremiumFiles, downloadPremiumFile } from '../src/api.js'

vi.mock('../src/api.js', () => ({ openPremiumFiles: vi.fn(), downloadPremiumFile: vi.fn() }))
const root = document.createElement('div')
afterEach(() => { render(null, root); vi.resetAllMocks() })
const rows = [{ id: 'key1', label: 'Подписка', mask: '••••1234' }]
const account = { key_id: 'key1', account: { available_countries: [{ server_country_code: 'nl', server_country_name: 'Нидерланды' }], active_device_count: 1, max_device_count: 5 } }

it('выпускает и скачивает внутри консоли без перехода на другой сайт', async () => {
  openPremiumFiles.mockResolvedValueOnce(account).mockResolvedValueOnce({ ...account, account: { ...account.account, active_device_count: 2, issued_configs: [{ server_country_code: 'nl', source_type: 'country_config' }] } })
  downloadPremiumFile.mockResolvedValue({ filename: 'amnezia_nl.conf', conf: '[Interface]\n[Peer]\n' })
  URL.createObjectURL = vi.fn(() => 'blob:synthetic')
  URL.revokeObjectURL = vi.fn()
  render(<AmneziaDashboard routerID={7} rows={rows} perms={{ remove: true }} />, root)
  expect(openPremiumFiles).not.toHaveBeenCalled()
  await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  expect(root.textContent).toContain('Нидерланды')
  expect(downloadPremiumFile).not.toHaveBeenCalled()
  await act(async () => [...root.querySelectorAll('button')].find((b) => b.textContent === 'Выпустить .conf').click())
  expect(downloadPremiumFile).toHaveBeenCalledWith(7, 'key1', 'nl', 'ru')
  await vi.waitFor(() => expect(root.textContent).toContain('Занято мест: 2 из 5'))
  expect(root.textContent).toContain('уже выпущена')
  expect(openPremiumFiles).toHaveBeenCalledTimes(2)
  expect(root.querySelector('a[download]').getAttribute('href')).toBe('blob:synthetic')
  expect(root.querySelector('a[target],iframe')).toBeNull()
})

it('показывает отказ и не повторяет выпуск автоматически', async () => {
  openPremiumFiles.mockResolvedValue(account)
  downloadPremiumFile.mockRejectedValue({ serverMessage: 'Все места заняты' })
  render(<AmneziaDashboard routerID={7} rows={rows} perms={{ remove: true }} />, root)
  await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { cancelable: true })))
  await act(async () => [...root.querySelectorAll('button')].find((b) => b.textContent === 'Выпустить .conf').click())
  expect(root.querySelector('[role=alert]').textContent).toBe('Все места заняты')
  expect(downloadPremiumFile).toHaveBeenCalledTimes(1)
})

it('оператору не показывает форму ключа и выдачи файла', () => {
  render(<AmneziaDashboard routerID={7} rows={rows} perms={{ remove: false }} />, root)
  expect(root.querySelector('form')).toBeNull()
  expect(root.textContent).toContain('владельцу')
})

it('сохраняет готовый файл при отказе обновления подписки', async () => {
  openPremiumFiles.mockResolvedValueOnce(account).mockRejectedValueOnce(new Error('network'))
  downloadPremiumFile.mockResolvedValue({ filename: 'amnezia_nl.conf', conf: '[Interface]\n[Peer]\n' })
  URL.createObjectURL = vi.fn(() => 'blob:ready')
  URL.revokeObjectURL = vi.fn()
  render(<AmneziaDashboard routerID={7} rows={rows} perms={{ remove: true }} />, root)
  await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { cancelable: true })))
  await act(async () => [...root.querySelectorAll('button')].find((b) => b.textContent === 'Выпустить .conf').click())
  await vi.waitFor(() => expect(root.textContent).toContain('Данные подписки показаны до выпуска'))
  expect(root.querySelector('a[download]').href).toContain('blob:ready')
  expect(downloadPremiumFile).toHaveBeenCalledTimes(1)
})

it('предлагает явную установку выбранной страны и подписки на роутер', async () => {
  openPremiumFiles.mockResolvedValue(account)
  const onPick = vi.fn()
  render(<AmneziaDashboard routerID={7} rows={rows} perms={{ remove: true }} onPick={onPick} />, root)
  await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { cancelable: true })))
  const region = root.querySelector('[aria-label="Откуда подключаетесь"]')
  await act(async () => { region.value='ag'; region.dispatchEvent(new Event('change', { bubbles: true })) })
  const install = [...root.querySelectorAll('button')].find(b => b.textContent === 'Выпустить и положить на роутер')
  expect(install).toBeTruthy()
  await act(async () => install.click())
  expect(onPick).toHaveBeenCalledWith(expect.objectContaining({provider:'amnezia', keyID:'key1', declaredCountry:'ag', option:expect.objectContaining({id:'nl'})}))
  expect(downloadPremiumFile).not.toHaveBeenCalled()
})
