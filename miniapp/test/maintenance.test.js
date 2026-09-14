import { describe, it, expect } from 'vitest'
import {
  MAINT_TEXTS,
  mayMaintain,
  updatesAgentReady,
  updateRow,
  hrneoButtonVisible,
  awgmUpdateSheet,
  hrneoUpdateSheet,
  firmwareSheet,
  rebootSheet,
  restartSheet,
  opkgUpgradeSheet,
  feedDisableSheet,
  parseAwgmUpdate,
  awgmUpdateText,
  hrneoUpdateText,
  normalizeFeedURL,
  feedHost,
  opkgUpgradeOutcome,
  serviceRestartText,
  refusalFromResult,
  maintenanceOutcomeLabel,
  commandErrorText,
  asleepNote,
  commandDeadlineMs,
  rebootBannerVisible,
} from '../src/maintenance.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'

const INTERNAL = /awgm_update|hrneo_update|service_restart|opkg_|\bhrneo\b|\bopkg\b|\bawgmgr\b/

function visibleText(sheet) {
  return [sheet.title, sheet.body, sheet.buttonLabel, sheet.commandLabel].join(' | ')
}

const AWGM_OK = (extra) => ({
  status: 'ok',
  output: JSON.stringify({ updated: true, from: '2.19.0+r2', to: '2.19.1', kmod_installed: '3.2.20260930', kmod_loaded: '3.2.20260930', reboot_needed: false, ...extra }),
})

describe('кто и когда видит обслуживание', () => {
  it('админ, владелец и оператор -- да; без роли -- нет', () => {
    for (const role of ['admin', 'owner', 'operator']) expect(mayMaintain({ role })).toBe(true)
    expect(mayMaintain({ role: '' })).toBe(false)
    expect(mayMaintain(null)).toBe(false)
  })

  it('обновления -- только агенту от v0.32.0, отказ по умолчанию', () => {
    expect(updatesAgentReady({ agent_version: 'v0.32.0' })).toBe(true)
    expect(updatesAgentReady({ agent_version: 'v0.32.1' })).toBe(true)
    expect(updatesAgentReady({ agent_version: 'v0.31.1' })).toBe(false)
    expect(updatesAgentReady({ agent_version: 'v0.32.0-rc1' })).toBe(false)
    expect(updatesAgentReady({})).toBe(false)
  })

  it('строка новости по компоненту и HydraRoute без «не установлен»', () => {
    const versions = { rows: [{ component: 'awgmgr', installed: '2.19.0+r2', available: '2.19.1' }], installed: { hrneo_installed: true } }
    expect(updateRow(versions, 'awgmgr').available).toBe('2.19.1')
    expect(updateRow(versions, 'hrneo')).toBeNull()
    expect(hrneoButtonVisible(versions)).toBe(true)
    // Молчание опроса -- не «не установлен»: кнопка остаётся.
    expect(hrneoButtonVisible({ installed: {} })).toBe(true)
    expect(hrneoButtonVisible(null)).toBe(true)
    expect(hrneoButtonVisible({ installed: { hrneo_installed: false } })).toBe(false)
  })
})

describe('листы', () => {
  it('awg-manager: «было → станет», обычная кнопка, без набора имени', () => {
    const s = awgmUpdateSheet({ routerID: 2, row: { installed: '2.19.0+r2', available: '2.19.1' } })
    expect(s.action).toBe('awgm_update')
    expect(s.body).toContain('2.19.0+r2 → 2.19.1')
    expect(s.body).toContain('VPN-туннели на несколько секунд переподключатся')
    expect(s.buttonLabel).toBe('Обновить')
    expect(s.confirmPhrase).toBe('')
    expect(s.danger).toBe(false)
  })

  it('HydraRoute Neo: кнопка обновления без внешней «последней версии»', () => {
    const s = hrneoUpdateSheet({ routerID: 2, installed: '3.18.3' })
    expect(s.action).toBe('hrneo_update')
    expect(s.body).toContain('3.18.3 →')
    expect(s.body).toContain('переподключатся')
    expect(s.confirmPhrase).toBe('')
  })

  it('прошивка и перезагрузка требуют набрать имя роутера', () => {
    const fw = firmwareSheet({ routerID: 2, routerName: 'home', current: '5.02.A.8.0-3', available: '5.02.A.9.0-0' })
    expect(fw.action).toBe('firmware_install')
    expect(fw.confirmPhrase).toBe('home')
    expect(fw.danger).toBe(true)
    const rb = rebootSheet({ routerID: 2, routerName: 'home' })
    expect(rb.action).toBe('service_restart')
    expect(rb.args).toEqual({ name: 'router' })
    expect(rb.confirmPhrase).toBe('home')
    expect(rb.danger).toBe(true)
  })

  it('перезапуск служб -- обычный лист, пакеты Entware -- с предупреждением', () => {
    expect(restartSheet({ routerID: 2, name: 'hrneo' }).args).toEqual({ name: 'hrneo' })
    expect(restartSheet({ routerID: 2, name: 'awgmgr' }).args).toEqual({ name: 'awgmgr' })
    expect(restartSheet({ routerID: 2, name: 'hrneo' }).confirmPhrase).toBe('')
    const up = opkgUpgradeSheet({ routerID: 2 })
    expect(up.action).toBe('opkg_upgrade')
    expect(up.danger).toBe(true)
    expect(up.body).toContain('перестанут отвечать')
    const feed = feedDisableSheet({ routerID: 2, feed: { url: 'https://feed.example.com/aarch64-k3.10', host: 'feed.example.com' } })
    expect(feed.action).toBe('opkg_feed_disable')
    expect(feed.args).toEqual({ url: 'https://feed.example.com/aarch64-k3.10' })
    expect(feed.buttonLabel).toBe('Отключить фид')
  })

  it('ни один лист не показывает внутренних имён', () => {
    const sheets = [
      awgmUpdateSheet({ routerID: 2, row: { installed: '1', available: '2' } }),
      hrneoUpdateSheet({ routerID: 2 }),
      firmwareSheet({ routerID: 2, routerName: 'home', current: '1', available: '2' }),
      rebootSheet({ routerID: 2, routerName: 'home' }),
      restartSheet({ routerID: 2, name: 'hrneo' }),
      restartSheet({ routerID: 2, name: 'awgmgr' }),
      opkgUpgradeSheet({ routerID: 2 }),
      feedDisableSheet({ routerID: 2, feed: { url: 'https://feed.example.com/x', host: 'feed.example.com' } }),
    ]
    for (const s of sheets) {
      expect(s.commandLabel).toBeTruthy()
      expect(visibleText(s)).not.toMatch(INTERNAL)
    }
  })
})

describe('итоги', () => {
  it('awg-manager: обновлён, последняя версия, нужна перезагрузка', () => {
    expect(awgmUpdateText(AWGM_OK())).toBe('awg-manager обновлён: 2.19.0+r2 → 2.19.1.')
    expect(awgmUpdateText(AWGM_OK({ kmod_loaded: '3.1.20260906', reboot_needed: true }))).toBe(
      'awg-manager обновлён: 2.19.0+r2 → 2.19.1. Сменился модуль ядра — нужна перезагрузка роутера.',
    )
    expect(awgmUpdateText({ status: 'ok', output: '{"updated":false,"from":"2.19.1","to":"2.19.1"}' })).toBe('Уже стоит последняя версия awg-manager.')
    expect(parseAwgmUpdate(AWGM_OK({ reboot_needed: true })).rebootNeeded).toBe(true)
    expect(parseAwgmUpdate({ status: 'err', output: 'x' })).toBeNull()
  })

  it('awg-manager: ошибки словами, без путей API', () => {
    expect(awgmUpdateText({ status: 'err', output: 'awg-manager не вернулся с новой версией за 5 минут' })).toBe(
      'awg-manager не вернулся с новой версией за 5 минут.',
    )
    expect(awgmUpdateText({ status: 'err', output: 'awg-manager отказался обновляться: awgmgr /api/system/update/apply: HTTP 409: busy' })).toBe(
      'awg-manager отказался обновляться.',
    )
    const generic = awgmUpdateText({ status: 'err', output: 'проверка обновления awg-manager: awgmgr GET /api/system/update/check: HTTP 500' })
    expect(generic).toBe('Не удалось обновить awg-manager — роутер ответил ошибкой.')
  })

  // Ruling M3: пока автоустановщик awg-manager сам считает обновление,
  // action/awgm_update.go возвращает errAwgmStillChecking -- это не отказ
  // роутера, и не должно читаться как "роутер ответил ошибкой".
  it('awg-manager: ещё проверяет обновления -- не общая ошибка', () => {
    expect(awgmUpdateText({ status: 'err', output: 'awg-manager ещё проверяет обновления — повторите через минуту' })).toBe(
      'awg-manager ещё проверяет обновления — повторите через минуту.',
    )
  })

  it('HydraRoute Neo', () => {
    expect(hrneoUpdateText({ status: 'ok', output: '{"updated":true,"from":"3.18.3-1","to":"3.19.0-1","running":true}' })).toBe(
      'HydraRoute Neo обновлён: 3.18.3-1 → 3.19.0-1.',
    )
    expect(hrneoUpdateText({ status: 'ok', output: '{"updated":false,"from":"3.18.3-1","to":"3.18.3-1","running":true}' })).toBe(
      'Уже стоит последняя версия HydraRoute Neo.',
    )
    expect(hrneoUpdateText({ status: 'err', output: 'HydraRoute Neo не установлен' })).toBe(MAINT_TEXTS.hrneoMissing)
    expect(hrneoUpdateText({ status: 'err', output: 'Не хватит места на /opt: …' })).toBe(MAINT_TEXTS.noSpace)
    expect(hrneoUpdateText({ status: 'locked', output: 'opkg lock held' })).toBe(MAINT_TEXTS.busy)
    expect(hrneoUpdateText({ status: 'err', output: 'opkg upgrade hrneo: exit status 1' })).not.toMatch(INTERNAL)
  })

  it('пакеты Entware: мёртвые фиды из payload, как у бота', () => {
    const out = opkgUpgradeOutcome({
      status: 'ok',
      output: '✅ Все пакеты актуальны — обновлять нечего.\n\n⚠️ Недоступные фиды:\n • https://feed.example.com/aarch64-k3.10/Packages.gz',
      payload: { failed_feeds: ['https://feed.example.com/aarch64-k3.10/Packages.gz'] },
    })
    expect(out.text).toBe('Все пакеты актуальны — обновлять нечего.')
    expect(out.tone).toBe('warn')
    expect(out.failedFeeds).toEqual([{ url: 'https://feed.example.com/aarch64-k3.10', host: 'feed.example.com' }])
    expect(opkgUpgradeOutcome({ status: 'ok', output: '✅ Обновлено пакетов: 3 (~1.2 MB)\nСписок: a, b, c' }).text).toBe('Обновлено пакетов: 3 (~1.2 MB)')
    expect(opkgUpgradeOutcome({ status: 'err', output: '❌ Не хватит места на /opt.' }).text).toBe(MAINT_TEXTS.noSpace)
    expect(opkgUpgradeOutcome({ status: 'locked', output: '' }).text).toBe(MAINT_TEXTS.busy)
    expect(opkgUpgradeOutcome({ status: 'ok', output: '🔧 Фид https://feed.example.com уже отключён или не найден в opkg-конфигах.' }).text).toBe(
      'Этот источник пакетов уже отключён.',
    )
    expect(opkgUpgradeOutcome(null)).toBeNull()
    expect(normalizeFeedURL('https://feed.example.com/x/Packages.gz')).toBe('https://feed.example.com/x')
    expect(feedHost('https://feed.example.com:8443/x')).toBe('feed.example.com')
  })

  it('перезапуск, перезагрузка и отказ агента', () => {
    expect(serviceRestartText('hrneo', { status: 'ok', output: 'hrneo restart sent' })).toBe('HydraRoute Neo перезапущен.')
    expect(serviceRestartText('awgmgr', { status: 'ok', output: '' })).toBe('awg-manager перезапущен.')
    expect(serviceRestartText('router', { status: 'ok', output: '' })).toContain('перезагрузку')
    const refusal = { status: 'err', output: 'router reboot disabled in agent config' }
    expect(serviceRestartText('router', refusal)).toBe(MAINT_TEXTS.rebootForbidden)
    expect(refusalFromResult(refusal)).toEqual({ kind: 'reboot', text: MAINT_TEXTS.rebootForbidden })
    expect(refusalFromResult({ status: 'err', output: 'firmware install disabled in agent config' })).toEqual({
      kind: 'firmware',
      text: MAINT_TEXTS.firmwareForbidden,
    })
    expect(refusalFromResult({ status: 'ok', output: 'router reboot disabled in agent config' })).toBeNull()
  })

  // Итог прошивки: агентский отказ по настройкам -- своей фразой (refusalFromResult),
  // успех и любая ДРУГАЯ ошибка агента -- нейтральным текстом, без сырого
  // вывода (например "ndmc components commit: exit status 1").
  it('прошивка: отказ по настройкам, успех и прочая ошибка -- без сырого вывода агента', () => {
    expect(maintenanceOutcomeLabel('firmware_install', { status: 'ok', output: 'firmware install started' })).toBe(
      'Роутер ставит прошивку и перезагрузится.',
    )
    expect(maintenanceOutcomeLabel('firmware_install', { status: 'err', output: 'ndmc components commit: exit status 1' })).toBe(
      'Не удалось поставить прошивку.',
    )
    expect(maintenanceOutcomeLabel('firmware_install', { status: 'err', output: 'firmware install disabled in agent config' })).toBe(
      MAINT_TEXTS.firmwareForbidden,
    )
  })

  it('maintenanceOutcomeLabel выбирает по действию и молчит о чужих', () => {
    expect(maintenanceOutcomeLabel('awgm_update', AWGM_OK())).toBe('awg-manager обновлён: 2.19.0+r2 → 2.19.1.')
    expect(maintenanceOutcomeLabel('service_restart', { status: 'ok' }, { name: 'awgmgr' })).toBe('awg-manager перезапущен.')
    expect(maintenanceOutcomeLabel('hrneo_update', { status: 'err', output: 'unknown action: hrneo_update' })).toBe(AGENT_OLDER_THAN_APP)
    expect(maintenanceOutcomeLabel('tunnel_restart', { status: 'ok' })).toBe('')
  })

  it('замок пакетов -- русский текст независимо от действия и от английского вывода', () => {
    // hrneo_update и opkg_* делят один замок opkg на роутере (agent lock),
    // и при отказе агент отвечает статусом locked с английским текстом --
    // экран обязан пересказать его по-русски, не подставляя это в вывод.
    expect(maintenanceOutcomeLabel('hrneo_update', { status: 'locked', output: 'opkg lock held' })).toBe(MAINT_TEXTS.busy)
    expect(maintenanceOutcomeLabel('opkg_upgrade', { status: 'locked', output: 'opkg lock held' })).toBe(MAINT_TEXTS.busy)
    expect(maintenanceOutcomeLabel('opkg_feed_disable', { status: 'locked', output: 'opkg lock held' })).not.toMatch(INTERNAL)
  })
})

describe('ответы сервера и ожидание', () => {
  it('коды отказа словами', () => {
    expect(commandErrorText('agent_too_old')).toContain('обновления агента')
    expect(commandErrorText('confirm_mismatch')).toBe('Имя роутера набрано неверно — команда не отправлена.')
    expect(commandErrorText('reboot_cooldown')).toBe('Роутер уже перезагружается — повторить можно через пять минут.')
    expect(commandErrorText('whatever')).toBe('')
  })

  it('роутер спит -- экран называет окно ожидания', () => {
    expect(asleepNote({ cmd_id: 'c1', router_asleep: true, wake_window_min: 10 })).toBe(
      'Роутер спит — команда выполнится, если он проснётся в течение 10 минут.',
    )
    expect(asleepNote({ cmd_id: 'c1', router_asleep: true, router_status: 'sleeping', wake_window_min: 10 })).toBe(
      'Роутер спит — команда выполнится, если он проснётся в течение 10 минут.',
    )
    expect(asleepNote({ cmd_id: 'c1' })).toBe('')
  })

  it('роутер не на связи -- экран называет ту же паузу другими словами', () => {
    expect(asleepNote({ cmd_id: 'c1', router_asleep: true, router_status: 'offline', wake_window_min: 10 })).toBe(
      'Роутер не на связи — команда выполнится, если он появится в течение 10 минут.',
    )
  })

  it('долгие действия ждут дольше короткого', () => {
    expect(commandDeadlineMs('awgm_update', false)).toBe(7 * 60_000)
    expect(commandDeadlineMs('hrneo_update', false)).toBe(6 * 60_000)
    expect(commandDeadlineMs('service_restart', false)).toBe(90_000)
    expect(commandDeadlineMs('service_restart', true)).toBe(90_000 + 5 * 60_000)
  })

  it('плашка перезагрузки: из снимка или сразу из итога обновления', () => {
    expect(rebootBannerVisible({ versions: { reboot_hint: 'x' }, awgmResult: null })).toBe(true)
    expect(rebootBannerVisible({ versions: {}, awgmResult: AWGM_OK({ reboot_needed: true }) })).toBe(true)
    expect(rebootBannerVisible({ versions: {}, awgmResult: AWGM_OK() })).toBe(false)
    expect(rebootBannerVisible({ versions: null, awgmResult: null })).toBe(false)
  })
})
