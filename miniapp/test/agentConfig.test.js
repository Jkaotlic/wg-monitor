// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
// h, а не JSX: файл по плану остаётся .js, и разбор JSX в нём не включён.
import { h, render } from 'preact'
import { act } from 'preact/test-utils'
import {
  AGENT_CONFIG_MIN_VERSION,
  AGENT_CONFIG_TEXTS,
  agentConfigAvailable,
  agentConfigArgs,
  agentConfigFields,
  agentConfigRows,
  editableAgentConfigKeys,
  validateAgentConfig,
} from '../src/agentConfig.js'

// Ответ агента (actions.AgentConfigView): путь DoH приезжает уже
// замаскированным -- маскирует его сам роутер, https://<host>/***.
const VIEW = {
  config_kind: 'agent',
  interval_sec: 120,
  awgm_base_url: 'http://192.168.31.1:8080',
  awgm_login: 'admin',
  external_reach_enabled: true,
  external_reach_fail_threshold: 3,
  allow_router_reboot: false,
  allow_firmware_install: false,
  dns_watchdog_enabled: false,
  dns_watchdog_endpoint: 'https://dns.example.com/***',
  dns_watchdog_canary_domain: 'example.com',
  dns_watchdog_bootstrap_ip: '198.51.100.10',
  config_path: '/opt/etc/wg-monitor/config.yaml',
}

describe('маскировка секретов', () => {
  it('путь своего DNS-сервера не показываем — только «задано»', () => {
    const rows = agentConfigRows({ dns_watchdog_endpoint: 'https://dns.example.com/***' })
    const row = rows.find((r) => r.key === 'watchdog_endpoint')
    expect(row.value).toBe('задано')
    expect(JSON.stringify(rows)).not.toContain('dns.example.com')
  })

  it('нет своего DNS-сервера — так и говорим', () => {
    const rows = agentConfigRows({ dns_watchdog_endpoint: '' })
    expect(rows.find((r) => r.key === 'watchdog_endpoint').value).toBe('не задано')
  })

  it('скрытое значение вписать назад нельзя', () => {
    expect(validateAgentConfig({ dns_watchdog_endpoint: 'https://x/***' }).error).toBe(
      'Чтобы изменить, впишите настоящий адрес целиком. Скрытое значение вписать назад нельзя.',
    )
    // То же правило для любого поля: маска затёрла бы настоящий секрет на роутере.
    expect(validateAgentConfig({ awgm_base_url: 'http://***' }).error).toBe(
      AGENT_CONFIG_TEXTS.maskedBack,
    )
  })

  it('пароля панели на экране нет вовсе', () => {
    expect(JSON.stringify(agentConfigRows({ awgm_login: 'admin' }))).not.toContain('password')
    expect(JSON.stringify(agentConfigRows(VIEW))).not.toContain('password')
    expect(JSON.stringify(agentConfigFields())).not.toContain('password')
  })

  it('ни одно скрытое значение не уезжает назад аргументом', () => {
    const args = agentConfigArgs(VIEW, { dns_watchdog_endpoint: 'https://dns.example.com/***' })
    expect(JSON.stringify(args)).not.toContain('***')
  })
})

describe('что мини-апп правит', () => {
  // Четыре ключа сторожа в v0.31 из мини-аппа не правятся: среди них
  // dns_watchdog_enabled -- выключатель сторожа, а ворота нулевого пункта
  // этот цикл не делает.
  it('мини-апп правит семь ключей из одиннадцати', () => {
    const editable = editableAgentConfigKeys()
    expect(editable).toHaveLength(7)
    for (const k of [
      'dns_watchdog_enabled',
      'dns_watchdog_endpoint',
      'dns_watchdog_canary_domain',
      'dns_watchdog_bootstrap_ip',
    ]) {
      expect(editable).not.toContain(k)
    }
  })

  it('форма показывает ровно то, что правит', () => {
    expect(agentConfigFields().map((f) => f.key)).toEqual(editableAgentConfigKeys())
  })

  // Прямое решение оператора: перенаправление адреса бэкенда -- захват всего
  // парка, и запрет живёт там, где его не обойти правкой сервера.
  it('адрес бэкенда не правится ни под каким именем', () => {
    const editable = editableAgentConfigKeys()
    for (const k of ['backend_url', 'url', 'backend_token', 'token']) {
      expect(editable).not.toContain(k)
    }
    expect(validateAgentConfig({ backend_url: 'https://evil.example.com' }).error).toBe(
      AGENT_CONFIG_TEXTS.notEditable,
    )
    expect(agentConfigArgs(VIEW, { backend_url: 'https://evil.example.com' })).toEqual({})
  })

  it('ключи сторожа не правятся и аргументом', () => {
    expect(agentConfigArgs(VIEW, { dns_watchdog_enabled: true })).toEqual({})
    expect(validateAgentConfig({ dns_watchdog_enabled: true }).error).toBe(
      AGENT_CONFIG_TEXTS.notEditable,
    )
  })

  it('уезжает только изменённое, а не вся форма', () => {
    expect(agentConfigArgs(VIEW, { interval_sec: 300 })).toEqual({ interval_sec: 300 })
    expect(agentConfigArgs(VIEW, { interval_sec: 120 })).toEqual({})
  })

  it('негодные числа не уходят на роутер', () => {
    expect(validateAgentConfig({ interval_sec: 5 }).error).toBeTruthy()
    expect(validateAgentConfig({ external_reach_fail_threshold: 99 }).error).toBeTruthy()
    expect(validateAgentConfig({ awgm_base_url: 'не адрес' }).error).toBeTruthy()
    expect(validateAgentConfig({ interval_sec: 300 }).error).toBe('')
  })
})

// ВТОРАЯ из двух независимых преград (решение оператора п. 10): для роутера с
// агентом ниже пола экран не рисуется вовсе. Гейт бэкенда при этом остаётся
// на месте -- ни одна из преград не заменяет другую.
describe('кому и когда экран доступен', () => {
  it('только админу и только с агентом от пола версии', () => {
    expect(agentConfigAvailable({ role: 'admin', agent_version: AGENT_CONFIG_MIN_VERSION })).toBe(true)
    expect(agentConfigAvailable({ role: 'admin', agent_version: 'v0.31.4' })).toBe(true)
    expect(agentConfigAvailable({ role: 'owner', agent_version: 'v0.31.0' })).toBe(false)
    expect(agentConfigAvailable({ role: 'operator', agent_version: 'v0.31.0' })).toBe(false)
  })

  it('версия ниже пола, неизвестная и нечитаемая — отказ по умолчанию', () => {
    expect(agentConfigAvailable({ role: 'admin', agent_version: 'v0.30.1' })).toBe(false)
    expect(agentConfigAvailable({ role: 'admin', agent_version: '' })).toBe(false)
    expect(agentConfigAvailable({ role: 'admin' })).toBe(false)
    expect(agentConfigAvailable({ role: 'admin', agent_version: 'мусор' })).toBe(false)
    expect(agentConfigAvailable(null)).toBe(false)
  })
})

// Тот же гейт в настоящем экране: не «рисуется и ругается», а не рисуется.
const mocks = vi.hoisted(() => ({ settings: null, command: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({
    busy: false,
    result: mocks.command,
    error: null,
    errorCode: null,
    run: () => Promise.resolve(mocks.command),
  }),
}))

const { AgentConfigScreen } = await import('../src/screens/AgentConfigScreen.jsx')

async function mount(settings) {
  mocks.settings = settings
  mocks.command = { status: 'ok', output: JSON.stringify(VIEW) }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      h(AgentConfigScreen, { routerID: 1, routerName: 'vpn-nl', onClose: () => {}, openSheet: () => {} }),
      root,
    )
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return root
}

describe('экран правки конфига агента', () => {
  it('у старого агента полей нет вовсе, и сказано почему', async () => {
    const root = await mount({ role: 'admin', agent_version: 'v0.30.1' })
    expect(root.querySelectorAll('input')).toHaveLength(0)
    expect(root.textContent).toContain(AGENT_CONFIG_TEXTS.tooOld)
    // Серой кнопки нет: кнопка, которая ничего не делает, хуже её отсутствия.
    expect(root.textContent).not.toContain('Сохранить настройки')
    render(null, root)
  })

  it('версии не знаем — тоже не рисуем', async () => {
    const root = await mount({ role: 'admin', agent_version: '' })
    expect(root.querySelectorAll('input')).toHaveLength(0)
    render(null, root)
  })

  it('не админу экран не рисуется вовсе', async () => {
    const root = await mount({ role: 'owner', agent_version: 'v0.31.0' })
    expect(root.querySelectorAll('input')).toHaveLength(0)
    render(null, root)
  })

  it('админу с новым агентом рисуется форма и говорится о перезапуске', async () => {
    const root = await mount({ role: 'admin', agent_version: 'v0.31.0' })
    expect(root.querySelectorAll('input').length).toBeGreaterThan(0)
    expect(root.textContent).toContain(AGENT_CONFIG_TEXTS.restart)
    expect(root.textContent).toContain(AGENT_CONFIG_TEXTS.panelPassword)
    // Путь своего DNS-сервера не появляется на экране ни в каком виде.
    expect(root.textContent).not.toContain('dns.example.com')
    render(null, root)
  })
})
