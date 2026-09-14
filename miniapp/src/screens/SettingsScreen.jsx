import { useEffect, useState } from 'preact/hooks'
import { fetchRouterSettings, fetchRouterChecks, setRouterNotify, fetchRouterVersions, setUpdateReminder, createPanelTicket } from '../api.js'
import { openExternal } from '../telegram.js'
import { useCommand } from '../useCommand.js'
import { thresholdRows, auditRows, doctorRows, pingRows, firmwareStatus, panelRow, panelOpenURL } from '../settings.js'
import { versionsRows, unknownLine, installedRows } from '../versions.js'
import { humanAge } from '../labels.js'
import { confirmSheet, localSheet } from '../sheet.js'
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
  opkgUpgradeOutcome,
  refusalFromResult,
  rebootBannerVisible,
} from '../maintenance.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'

// Настройки роутера и обслуживание -- то, за чем оператор раньше шёл в бота.
//
// Экран ничего не настраивает в самом приложении: настраивать там нечего.
// Он показывает, по каким правилам бот судит об этом роутере (числа живут в
// backend.yaml), что на роутере стоит из версий, что можно спросить у него
// прямо сейчас и -- с цикла 1 -- обновить, перезапустить и перезагрузить.
export function SettingsScreen({ routerID, routerName, asleep, openSheet, onClose }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [error, setError] = useState(null)
  const [showHelp, setShowHelp] = useState(false)
  // Версии живут своим запросом: если срез не ответил, экран настроек обязан
  // остаться рабочим, а не погаснуть целиком из-за новостей.
  const [versions, setVersions] = useState(null)
  const [versionsError, setVersionsError] = useState(null)
  const [newsBusy, setNewsBusy] = useState(false)

  const [panelBusy, setPanelBusy] = useState(false)
  const [panelNote, setPanelNote] = useState('')
  const [panelError, setPanelError] = useState('')

  const [notifyBusy, setNotifyBusy] = useState(false)
  const [notifyError, setNotifyError] = useState(null)

  // Итоги листов обслуживания. Лист закрывается, а экран обязан помнить:
  // обновление awg-manager с reboot_needed сразу зажигает плашку перезагрузки,
  // мёртвый фид из итога пакетов даёт кнопку «Отключить фид», отказ агента
  // меняет кнопку на объяснение.
  const [awgmResult, setAwgmResult] = useState(null)
  const [opkgResult, setOpkgResult] = useState(null)
  const [refusals, setRefusals] = useState({})

  const audit = useCommand(routerID)
  const firmware = useCommand(routerID)
  const install = useCommand(routerID)
  const doctor = useCommand(routerID)
  const hrneo = useCommand(routerID)
  const pingNow = useCommand(routerID)

  function load() {
    return Promise.all([fetchRouterSettings(routerID), fetchRouterChecks(routerID)])
      .then(([s, c]) => {
        setSettings(s)
        setTunnels(c.tunnels ?? [])
        setError(null)
      })
      .catch(() => setError('Не удалось прочитать настройки роутера.'))
  }

  function loadVersions() {
    return fetchRouterVersions(routerID)
      .then((v) => {
        setVersions(v)
        setVersionsError(null)
      })
      .catch(() => setVersionsError('Не удалось прочитать версии роутера.'))
  }

  // «Отложить» и «скрыть» -- решение про весь роутер, и сервер пустит сюда
  // только владельца и админа. Версию считает он же.
  const hideNews = (component, action) => {
    setNewsBusy(true)
    setVersionsError(null)
    return setUpdateReminder(routerID, component, action)
      .then(() => loadVersions())
      .catch(() => setVersionsError('Не удалось сохранить. Попробуйте ещё раз.'))
      .finally(() => setNewsBusy(false))
  }

  useEffect(() => {
    load()
    loadVersions()
  }, [routerID])

  // Билет берётся в момент нажатия, а не заранее: он живёт минуту и
  // одноразовый. Браузер открывается снаружи Telegram, где сессии приложения
  // нет, -- поэтому и нужен билет.
  const openPanel = () => {
    setPanelBusy(true)
    setPanelError('')
    setPanelNote('')
    return createPanelTicket(routerID)
      .then((t) => {
        const url = panelOpenURL(t?.open_path, window.location.origin)
        if (!url) throw new Error('bad ticket')
        openExternal(url)
        setPanelNote('Панель откроется во внешнем браузере. Не открылась — нажмите кнопку ещё раз.')
      })
      .catch(() => setPanelError('Не удалось открыть панель. Попробуйте ещё раз.'))
      .finally(() => setPanelBusy(false))
  }

  const noteRefusal = (res) => {
    const refusal = refusalFromResult(res)
    if (refusal) setRefusals((prev) => ({ ...prev, [refusal.kind]: refusal.text }))
  }

  const auditOut = audit.result?.status === 'ok' ? auditRows(audit.result.output) : []
  const doctorOut = doctor.result?.status === 'ok' ? doctorRows(doctor.result.output) : []
  const hrneoOut = hrneo.result?.status === 'ok' ? doctorRows(hrneo.result.output) : []
  const pings = pingRows(tunnels)
  const fw = firmware.result?.status === 'ok' ? firmwareStatus(firmware.result.output) : null
  // Обслуживание -- админу, владельцу и операторам: тот же круг, что у сервера.
  // Прошивку с цикла 1 ставят и операторы (решение оператора 14.09).
  const maintain = mayMaintain(settings)
  const agentReady = updatesAgentReady(settings)
  // Скрыть новость может только владелец и админ: строка живёт на роутере, и
  // оператор убрал бы её с экрана владельца тоже.
  const mayHideNews = settings?.role === 'owner' || settings?.role === 'admin'
  const newsRows = versionsRows(versions)
  const showReboot = rebootBannerVisible({ versions, awgmResult })
  const opkgOut = opkgUpgradeOutcome(opkgResult)
  // Метка времени обязательна рядом с «проверить не удалось»: обещание без
  // неё говорит больше, чем мы знаем.
  const checkedAgo = versions?.checked_at
    ? `${humanAge(Math.max(0, Math.floor((Date.now() - new Date(versions.checked_at).getTime()) / 1000)))} назад`
    : ''
  const unknownLines = (versions?.unknown ?? []).map((u) => unknownLine(u.reason, checkedAgo)).filter(Boolean)

  // Кнопка в строке новости -- по компоненту. Сырые строки новостей
  // (versions.rows) несут installed/available, по ним и собирается лист.
  const newsAction = (component) => {
    if (!maintain || !openSheet) return null
    if (component === 'awgmgr' && agentReady) {
      const row = updateRow(versions, 'awgmgr')
      return { label: 'Обновить awg-manager', open: () => openSheet(awgmUpdateSheet({ routerID, row, asleep, onResult: setAwgmResult })) }
    }
    if (component === 'hrneo' && agentReady && hrneoButtonVisible(versions)) {
      const row = updateRow(versions, 'hrneo')
      return { label: 'Обновить HydraRoute Neo', open: () => openSheet(hrneoUpdateSheet({ routerID, installed: row.installed, available: row.available, asleep })) }
    }
    if (component === 'firmware' && !refusals.firmware) {
      const row = updateRow(versions, 'firmware')
      return {
        label: 'Установить прошивку',
        open: () => openSheet(firmwareSheet({ routerID, routerName, current: row.installed, available: row.available, asleep, onResult: noteRefusal, onDone: load })),
      }
    }
    return null
  }

  // Выключение уведомлений -- единственное действие на этом экране, которое
  // человек делает СЕБЕ, а не роутеру. Поэтому и предупреждение здесь про
  // последствие для него: бот замолчит, и поломку он увидит только сам.
  const toggleNotify = () => {
    const nextMuted = !settings?.notify_muted
    const apply = () => {
      setNotifyBusy(true)
      setNotifyError(null)
      return setRouterNotify(routerID, nextMuted)
        .then(() => load())
        .catch(() => setNotifyError('Не удалось сохранить. Попробуйте ещё раз.'))
        .finally(() => setNotifyBusy(false))
    }
    if (!nextMuted || !openSheet) {
      apply()
      return
    }
    openSheet(
      localSheet({
        title: 'Выключить уведомления об этом роутере?',
        body:
          `Бот перестанет писать вам про «${routerName}». О поломке вы узнаете, ` +
          'только сами открыв приложение. Остальные получатели этого роутера ' +
          'продолжат получать уведомления.',
        buttonLabel: 'Выключить',
        danger: true,
        perform: apply,
      }),
    )
  }

  // Включение и выключение проверки связи -- переключатель, а не правка
  // конфига: обратное действие стоит на той же строке.
  const askPingToggle = (row) => {
    openSheet(
      confirmSheet({
        routerID,
        title: row.enabled ? `Выключить проверку связи у «${row.title}»?` : `Включить проверку связи у «${row.title}»?`,
        body: row.enabled
          ? 'Роутер перестанет сам проверять этот VPN-туннель и поднимать его. Тревога о падении по-прежнему придёт — по обмену ключами.'
          : 'Роутер начнёт сам проверять VPN-туннель и поднимать его, если ответа не будет.',
        action: 'pingcheck_toggle',
        args: { tunnel_id: row.tunnelID, enable: !row.enabled },
        buttonLabel: row.enabled ? 'Выключить' : 'Включить',
        danger: Boolean(row.enabled),
        asleep,
        onDone: load,
      }),
    )
  }

  return (
    <Overlay title="Настройки" backLabel="Назад" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">{routerName || 'Роутер'}</h1>
        <p class="router-lastseen">Как бот судит об этом роутере и что на нём стоит.</p>

        {error && <p class="state state-error">{error}</p>}

        {settings && (
          <Section title="Опрос и тревоги">
            <div class="card">
              {thresholdRows(settings).map((r) => (
                <DataRow key={r.key} title={r.title} code={r.code} value={r.value} />
              ))}
              <p class="card-foot">
                Эти числа живут в настройках бота, а не роутера: поменять их можно там, где он
                запущен. Здесь они показаны, чтобы было видно, через сколько придёт тревога.
              </p>
            </div>
          </Section>
        )}

        <Section title="Уведомления">
          <div class="card settings-card">
            <DataRow
              title="Писать мне об этом роутере"
              value={settings?.notify_muted ? 'выключено' : 'включено'}
              valueTone={settings?.notify_muted ? 'warn' : 'ok'}
            />
            <p class="card-foot">
              {settings?.notify_muted
                ? 'Бот молчит об этом роутере. О поломке вы узнаете, только сами открыв приложение.'
                : 'Бот напишет вам в личку, когда с роутером что-то случится.'}
            </p>
          </div>
          <button
            type="button"
            class={settings?.notify_muted ? 'btn btn-ghost btn-wide' : 'btn btn-danger btn-wide'}
            disabled={notifyBusy}
            onClick={toggleNotify}
          >
            {notifyBusy ? 'Сохраняем…' : settings?.notify_muted ? 'Снова уведомлять' : 'Выключить уведомления'}
          </button>
          {notifyError && <p class="state state-error">{notifyError}</p>}
        </Section>

        <Section title="Обновления">
          {newsRows.length > 0 && (
            <div class="card settings-card">
              {newsRows.map((r) => {
                const act = newsAction(r.component)
                return (
                  <div key={r.key} class="settings-row">
                    <DataRow dot={r.tone} title={r.title} code={r.code} value={r.value} valueTone={r.tone} />
                    <p class="card-foot">{r.text}</p>
                    {act && (
                      <button type="button" class={r.component === 'firmware' ? 'btn btn-danger btn-row' : 'btn btn-primary btn-row'} onClick={act.open}>
                        {act.label}
                      </button>
                    )}
                    {r.component === 'firmware' && maintain && refusals.firmware && <p class="hint">{refusals.firmware}</p>}
                    {mayHideNews && (
                      <div class="settings-actions">
                        <button type="button" class="btn btn-ghost btn-row" disabled={newsBusy} onClick={() => hideNews(r.component, 'snooze')}>
                          Отложить на неделю
                        </button>
                        <button type="button" class="btn btn-ghost btn-row" disabled={newsBusy} onClick={() => hideNews(r.component, 'dismiss')}>
                          Скрыть эту новость
                        </button>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
          {/* Причины незнания -- отдельными строками. «Мы не знаем, что вышло»
              и «обновлений нет» обязаны звучать по-разному: раньше и то, и
              другое выглядело как отсутствие блока. */}
          {unknownLines.length > 0 && (
            <div class="card">
              {unknownLines.map((line) => (
                <p key={line} class="card-foot">{line}</p>
              ))}
            </div>
          )}
          {versions && newsRows.length === 0 && unknownLines.length === 0 && (
            <div class="card">
              <p class="card-foot">Обновлений нет: всё, что мы проверяем, на роутере свежее.</p>
            </div>
          )}
          {installedRows(versions).length > 0 && (
            <div class="card settings-card">
              {installedRows(versions).map((r) => (
                <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueSub={r.valueSub} valueTone={r.tone} />
              ))}
              {versions?.checked_at && <p class="card-foot">Роутер рассказал про версии {checkedAgo}.</p>}
            </div>
          )}
          {versionsError && <p class="state state-error">{versionsError}</p>}
        </Section>

        {maintain && openSheet && (
          <Section title="Обслуживание">
            {showReboot && (
              <div class="card settings-card">
                <p class="state state-warn">{MAINT_TEXTS.rebootBanner}</p>
                {refusals.reboot ? (
                  <p class="hint">{refusals.reboot}</p>
                ) : (
                  <button type="button" class="btn btn-danger btn-wide" onClick={() => openSheet(rebootSheet({ routerID, routerName, asleep, onResult: noteRefusal }))}>
                    Перезагрузить роутер
                  </button>
                )}
              </div>
            )}
            {!agentReady && <p class="hint">{MAINT_TEXTS.tooOld}</p>}
            {agentReady && hrneoButtonVisible(versions) && (
              <button
                type="button"
                class="btn btn-ghost btn-wide"
                onClick={() => openSheet(hrneoUpdateSheet({ routerID, installed: versions?.installed?.hrneo ?? '', asleep }))}
              >
                Проверить и обновить HydraRoute Neo
              </button>
            )}
            {!hrneoButtonVisible(versions) && <p class="hint">{MAINT_TEXTS.hrneoMissing}</p>}
            <div class="settings-actions">
              <button type="button" class="btn btn-ghost" onClick={() => openSheet(restartSheet({ routerID, name: 'hrneo', asleep }))}>
                Перезапустить HydraRoute
              </button>
              <button type="button" class="btn btn-ghost" onClick={() => openSheet(restartSheet({ routerID, name: 'awgmgr', asleep }))}>
                Перезапустить awg-manager
              </button>
            </div>
            <button type="button" class="btn btn-ghost btn-wide" onClick={() => openSheet(opkgUpgradeSheet({ routerID, asleep, onResult: setOpkgResult }))}>
              Обновить пакеты Entware
            </button>
            {opkgOut && (
              <div class="card settings-card">
                <p class={opkgOut.tone === 'error' ? 'state state-error' : opkgOut.tone === 'warn' ? 'state state-warn' : 'state'}>{opkgOut.text}</p>
                {opkgOut.failedFeeds.map((feed) => (
                  <div key={feed.url} class="settings-row">
                    <DataRow title="Источник пакетов не отвечает" value={feed.host} valueTone="warn" />
                    <button
                      type="button"
                      class="btn btn-ghost btn-row settings-row-btn"
                      onClick={() => openSheet(feedDisableSheet({ routerID, feed, asleep, onResult: setOpkgResult }))}
                    >
                      Отключить фид
                    </button>
                  </div>
                ))}
              </div>
            )}
          </Section>
        )}

        <Section title="Что стоит на роутере">
          <button type="button" class="btn btn-ghost btn-wide" disabled={audit.busy} onClick={() => audit.run('version_audit', {}, deadline).then((res) => { if (res?.status === 'ok') loadVersions() })}>
            {audit.busy ? 'Спрашиваем роутер…' : 'Сверить версии сейчас'}
          </button>
          {audit.error && <p class="state state-error">{audit.error}</p>}
          {audit.result && audit.result.status !== 'ok' && (
            <p class="state state-error">Роутер не ответил: {audit.result.output || audit.result.status}</p>
          )}
          {auditOut.length > 0 && (
            <div class="card settings-card">
              {auditOut.map((r) => (
                <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueSub={r.sub} valueTone={r.tone} />
              ))}
            </div>
          )}
        </Section>

        {/* Панель роутера -- владельцу и админу; оператору роутера секции нет
            вовсе. Адреса на экране нет: только «известна» и кнопка. */}
        {settings && (settings.role === 'owner' || settings.role === 'admin') && (() => {
          const panel = panelRow(settings)
          return (
            <Section title="Панель роутера">
              <div class="card settings-card">
                <DataRow title="Панель роутера" value={panel.value} valueTone={panel.known ? 'ok' : 'muted'} />
              </div>
              {panel.hint && <p class="hint">{panel.hint}</p>}
              {panel.known && (
                <>
                  <button type="button" class="btn btn-ghost btn-wide" disabled={panelBusy} onClick={openPanel}>
                    {panelBusy ? 'Открываем…' : 'Открыть панель роутера'}
                  </button>
                  <p class="hint">Панель спросит свой логин и пароль — мы их не знаем и не храним.</p>
                  {panelNote && <p class="hint">{panelNote}</p>}
                  {panelError && <p class="state state-error">{panelError}</p>}
                </>
              )}
            </Section>
          )
        })()}

        <Section title="Прошивка роутера">
          <button type="button" class="btn btn-ghost btn-wide" disabled={firmware.busy} onClick={() => firmware.run('firmware_status', {}, deadline)}>
            {firmware.busy ? 'Спрашиваем роутер…' : 'Проверить прошивку'}
          </button>
          {firmware.error && <p class="state state-error">{firmware.error}</p>}
          {firmware.result && firmware.result.status !== 'ok' && (
            <p class="state state-error">Роутер не ответил: {firmware.result.output || firmware.result.status}</p>
          )}
          {fw?.known && (
            <div class="card settings-card">
              {fw.rows.map((r) => (
                <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueTone={r.tone} />
              ))}
              {fw.hint && <p class="card-foot">Роутер говорит: {fw.hint}</p>}
              <p class="card-foot">
                Установка необратима: роутер скачает прошивку и перезагрузится. VPN-туннели упадут на
                несколько минут, и вернуть прежнюю версию из приложения нельзя.
              </p>
            </div>
          )}
          {fw?.known && fw.updateAvailable && maintain && openSheet && !refusals.firmware && (
            <button
              type="button"
              class="btn btn-danger btn-wide"
              onClick={() =>
                openSheet(firmwareSheet({ routerID, routerName, current: fw.current, available: fw.available, asleep, onResult: noteRefusal, onDone: load }))
              }
            >
              Установить прошивку
            </button>
          )}
          {fw?.known && fw.updateAvailable && maintain && refusals.firmware && <p class="hint">{refusals.firmware}</p>}
          {install.error && <p class="state state-error">{install.error}</p>}
        </Section>

        <Section title="Проверка связи">
          {pings.length === 0 ? (
            <div class="card">
              <p class="traffic-detail">Роутер не сообщил ни одного VPN-туннеля.</p>
            </div>
          ) : (
            <div class="card">
              {pings.map((r) => (
                <div key={r.key} class="settings-row">
                  <DataRow dot={r.tone === 'muted' ? undefined : r.tone} title={r.title} code={r.code} value={r.value} valueTone={r.tone === 'muted' ? undefined : r.tone} />
                  {r.enabled != null && openSheet && (
                    <button type="button" class="btn btn-ghost btn-row settings-row-btn" onClick={() => askPingToggle(r)}>
                      {r.enabled ? 'Выключить' : 'Включить'}
                    </button>
                  )}
                </div>
              ))}
              <p class="card-foot">
                Роутер сам проверяет VPN-туннель и поднимает его, если ответа нет. Задержка — это
                то, что он намерил последним замером.
              </p>
            </div>
          )}
          <button type="button" class="btn btn-ghost btn-wide" disabled={pingNow.busy} onClick={() => pingNow.run('pingcheck_now', {}, deadline).then((res) => { if (res?.status === 'ok') load() })}>
            {pingNow.busy ? 'Проверяем…' : 'Проверить связь сейчас'}
          </button>
          {pingNow.error && <p class="state state-error">{pingNow.error}</p>}
        </Section>

        <Section title="Проверить роутер изнутри">
          <div class="settings-actions">
            <button type="button" class="btn btn-ghost" disabled={doctor.busy} onClick={() => doctor.run('router_doctor', {}, deadline)}>
              {doctor.busy ? 'Смотрим…' : 'Осмотр роутера'}
            </button>
            <button type="button" class="btn btn-ghost" disabled={hrneo.busy} onClick={() => hrneo.run('hrneo_doctor', {}, deadline)}>
              {hrneo.busy ? 'Смотрим…' : 'Осмотр HydraRoute Neo'}
            </button>
          </div>
          {(doctor.error || hrneo.error) && <p class="state state-error">{doctor.error || hrneo.error}</p>}
          {[...doctorOut, ...hrneoOut].length > 0 && (
            <div class="card settings-card">
              {[...doctorOut, ...hrneoOut].map((r, i) => (
                <DataRow key={`${r.key}-${i}`} dot={r.tone} title={r.title} value={r.value} valueTone={r.tone} />
              ))}
            </div>
          )}
          {/* Доктор отвечает текстом; разобранные строки -- это его пересказ,
              и сам ответ обязан остаться доступным целиком. */}
          {(doctor.result?.status === 'ok' || hrneo.result?.status === 'ok') && (
            <>
              <button type="button" class="btn btn-ghost raw-toggle" onClick={() => setShowHelp((v) => !v)}>
                {showHelp ? 'Скрыть ответ целиком' : 'Ответ роутера целиком'}
              </button>
              {showHelp && (
                <pre class="raw-dump">{[doctor.result?.output, hrneo.result?.output].filter(Boolean).join('\n\n')}</pre>
              )}
            </>
          )}
        </Section>

        <Section title="Что умеет приложение">
          <div class="card">
            <p class="card-foot">
              <b>Сейчас</b> — работает ли обход прямо сейчас и что с ним не так.{' '}
              <b>VPN-туннели</b> — какой VPN-туннель несёт трафик, кто подхватит и что через него уходит.{' '}
              <b>Проверки</b> — те же вопросы, заданные роутеру заново, и адрес, которым вас
              видно снаружи. <b>Что было</b> — что происходило за неделю.{' '}
              <b>Настройки</b> — обновления, перезапуск служб и перезагрузка роутера.
            </p>
            <p class="card-foot">
              Уведомления остаются у бота: приложение не может разбудить того, кто его не открыл.
              Тревога придёт в чат, а из неё кнопка ведёт сюда.
            </p>
          </div>
        </Section>
      </div>
    </Overlay>
  )
}
