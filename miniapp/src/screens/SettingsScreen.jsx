import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchRouterSettings, setRouterNotify, fetchRouterVersions, setUpdateReminder } from '../api.js'
import { openExternal } from '../telegram.js'
import { useCommand } from '../useCommand.js'
import { thresholdRows, auditRows, firmwareStatus, panelRow, agentRow } from '../settings.js'
import { versionsRows, unknownLine, installedRows, checkedAtText } from '../versions.js'
import { localSheet } from '../sheet.js'
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
  firmwareStatusErrorText,
} from '../maintenance.js'
import { Section } from '../ui/Section.jsx'
import { ManageGroup } from '../ui/ManageGroup.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { HooksRow } from './SignalSections.jsx'
import { ErrorLine } from '../ui/ErrorLine.jsx'
import { manageAnchors, manageSection, manageSummaries, manageTones, versionsKnown } from '../manage.js'

// Настройки роутера и обслуживание -- то, за чем оператор раньше шёл в бота.
//
// Экран ничего не настраивает в самом приложении: настраивать там нечего.
// Он показывает, по каким правилам бот судит об этом роутере (числа живут в
// backend.yaml), что на роутере стоит из версий, что можно спросить у него
// прямо сейчас и -- с цикла 1 -- обновить, перезапустить и перезагрузить.
//
// С v0.41 это не слой за шестерёнкой, а содержимое вкладки «Управление»
// (ManageTab): разделы без своей крышки, первым -- адрес панели.
//
// С v0.52 это вкладка «Настройки»: четыре раздела по задаче человека --
// Обслуживание · Люди и уведомления · Роутер и агент · Опасное. Админские
// куски вкладка вставляет слотами (serviceSlot, peopleSlot, agentSlot,
// dangerSlot) -- так они стоят рядом с родственными, а не хвостом после справки.
export function SettingsSections({ routerID, routerName, asleep, openSheet, isAdmin = false, focusGroup = null, focusNonce = 0, serviceSlot = null, peopleSlot = null, agentSlot = null, dangerSlot = null }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [error, setError] = useState(null)
  // Версии живут своим запросом: если срез не ответил, экран настроек обязан
  // остаться рабочим, а не погаснуть целиком из-за новостей.
  const [versions, setVersions] = useState(null)
  const [versionsError, setVersionsError] = useState(null)
  const [newsBusy, setNewsBusy] = useState(false)

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

  function load() {
    return fetchRouterSettings(routerID)
      .then((s) => {
        setSettings(s)
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

  const noteRefusal = (res) => {
    const refusal = refusalFromResult(res)
    if (refusal) setRefusals((prev) => ({ ...prev, [refusal.kind]: refusal.text }))
  }

  const auditOut = audit.result?.status === 'ok' ? auditRows(audit.result.output) : []
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
  const checkedAgo = checkedAtText(versions?.checked_at)
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
    // routerName ещё не пришло (fleet не догрузился) -- confirmReady на пустой
    // фразе проходит без ввода (sheet.js), и сервер ответил бы confirm_mismatch.
    if (component === 'firmware' && !refusals.firmware && routerName) {
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

  // Какие разделы раскрыты (v0.52): по умолчанию свёрнуто всё; раздел по
  // старой ссылке или возврату из слоя -- раскрыт, раздел с заботой -- сам.
  const [openGroups, setOpenGroups] = useState(() => new Set([focusGroup].filter(Boolean)))
  const setGroup = (group, on) =>
    setOpenGroups((prev) => {
      if (prev.has(group) === on) return prev
      const next = new Set(prev)
      if (on) next.add(group)
      else next.delete(group)
      return next
    })
  // Прокрутка -- после перерисовки: раскрытая группа должна успеть вырасти.
  const scrollToAnchor = (id) => setTimeout(() => document.getElementById(id)?.scrollIntoView?.({ block: 'start', behavior: 'smooth' }), 0)
  useEffect(() => {
    if (!focusGroup) return
    setGroup(focusGroup, true)
    scrollToAnchor(`mg-${focusGroup}`)
  }, [focusGroup, focusNonce])


  const known = versionsKnown(versions)
  const anchors = manageAnchors({ isAdmin })
  const notes = manageSummaries({ settings, versions, showReboot, agentReady: settings ? agentReady : true, isAdmin })
  const tones = manageTones({ versions, showReboot, agentReady: settings ? agentReady : true })
  const group = (id) => ({ id: `mg-${id}`, title: manageSection(id).title, note: notes[id], noteTone: tones[id], open: openGroups.has(id), onToggle: (o) => setGroup(id, o) })
  // Группа с заботой раскрывается сама -- один раз, когда забота появилась;
  // закрытую человеком обратно не открываем.
  const autoOpened = useRef(new Set())
  useEffect(() => {
    for (const [group, tone] of Object.entries(tones)) {
      if (tone && !autoOpened.current.has(group)) {
        autoOpened.current.add(group)
        setGroup(group, true)
      }
    }
  }, [tones.service])
  const runAudit = () => audit.run('version_audit', {}, deadline).then((res) => { if (res?.status === 'ok') loadVersions() })
  const auditBlock = (
    <>
      <button type="button" class="btn btn-ghost btn-wide" disabled={audit.busy} onClick={runAudit}>
        {audit.busy ? 'Спрашиваем роутер…' : 'Сверить версии'}
      </button>
      <ErrorLine text={audit.error} busy={audit.busy} onRetry={runAudit} />
      {audit.result && audit.result.status !== 'ok' && <p class="state state-error">Роутер не ответил на сверку версий — попробуйте ещё раз.</p>}
      {auditOut.length > 0 && (
        <div class="card card-rows settings-card">
          {auditOut.map((r) => (
            <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueSub={r.sub} valueTone={r.tone} />
          ))}
        </div>
      )}
    </>
  )
  // «Перезагрузить роутер» -- постоянная кнопка (v0.52): раньше она появлялась
  // только вместе с плашкой «нужна перезагрузка», а перезагрузить роутер
  // можно и без неё. Права -- maintain: админ, владелец, оператор.
  const rebootBlock = maintain && openSheet && (
    <Section title="Перезагрузка роутера">
      {showReboot && <p class="state state-warn">{MAINT_TEXTS.rebootBanner}</p>}
      {refusals.reboot ? (
        <p class="hint">{refusals.reboot}</p>
      ) : routerName ? (
        <button type="button" class="btn btn-danger btn-wide" onClick={() => openSheet(rebootSheet({ routerID, routerName, asleep, onResult: noteRefusal }))}>
          Перезагрузить роутер
        </button>
      ) : null}
    </Section>
  )

  return (
    <>
      <nav class="manage-anchors" aria-label="Разделы «Настроек»">
        {anchors.map((a) => (
          <button
            key={a.id}
            type="button"
            class="manage-anchor"
            onClick={() => {
              setGroup(a.group, true)
              scrollToAnchor(a.id)
            }}
          >
            {a.label}
          </button>
        ))}
      </nav>
      <ErrorLine text={error} onRetry={load} />

      <ManageGroup {...group('service')}>
        {known || newsRows.length > 0 ? (
          <>
            <Section title="Что стоит на роутере">
              {agentRow(settings) && (
                <div class="card card-rows settings-card">
                  <DataRow title={agentRow(settings).title} value={agentRow(settings).value} />
                </div>
              )}
              {auditBlock}
            </Section>

            <Section title="Обновления">
              {newsRows.length > 0 && (
                <div class="card card-rows settings-card">
                  {newsRows.map((r) => {
                    const act = newsAction(r.component)
                    return (
                      // Новость -- столбиком (.settings-news): строка, текст,
                      // кнопка и пара «Отложить / Скрыть» друг под другом. В
                      // строку .settings-row они не влезали и уезжали за край.
                      <div key={r.key} class="settings-news">
                        <DataRow dot={r.tone} title={r.title} code={r.code} value={r.value} valueTone={r.tone} />
                        <p class="card-foot">{r.text}</p>
                        {act && (
                          <button type="button" class={r.component === 'firmware' ? 'btn btn-danger btn-row' : 'btn btn-ghost btn-row'} onClick={act.open}>
                            {act.label}
                          </button>
                        )}
                        {r.component === 'firmware' && maintain && refusals.firmware && <p class="hint">{refusals.firmware}</p>}
                        {mayHideNews && (
                          <div class="action-row">
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
                <div class="card card-rows">
                  {unknownLines.map((line) => (
                    <p key={line} class="card-foot">{line}</p>
                  ))}
                </div>
              )}
              {versions && newsRows.length === 0 && unknownLines.length === 0 && (
                <div class="card card-rows">
                  <p class="card-foot">Обновлений нет: всё, что мы проверяем, на роутере свежее.</p>
                </div>
              )}
              {installedRows(versions).length > 0 && (
                <div class="card card-rows settings-card">
                  {installedRows(versions).map((r) => (
                    <DataRow key={r.key} dot={r.tone} title={r.title} code={r.code} value={r.value} valueSub={r.valueSub} valueTone={r.tone} />
                  ))}
                  {versions?.checked_at && <p class="card-foot">Роутер рассказал про версии {checkedAgo}.</p>}
                </div>
              )}
              <ErrorLine text={versionsError} onRetry={loadVersions} />
            </Section>
          </>
        ) : (
          // Ничего не известно -- один блок, а не три «сведений нет» (п. 3.7).
          <Section title="Что стоит на роутере">
            <div class="card versions-empty">
              <p class="traffic-detail">Версии ещё не получены.</p>
              {settings?.agent_version && <p class="hint">Агент на роутере: {settings.agent_version}.</p>}
              {[...new Set(unknownLines)].map((line) => (
                <p key={line} class="hint">
                  {line}
                </p>
              ))}
              {auditBlock}
              <ErrorLine text={versionsError} onRetry={loadVersions} />
            </div>
          </Section>
        )}
        <Section title="Прошивка роутера">
          <button type="button" class="btn btn-ghost btn-wide" disabled={firmware.busy} onClick={() => firmware.run('firmware_status', {}, deadline)}>
            {firmware.busy ? 'Спрашиваем роутер…' : 'Проверить прошивку'}
          </button>
          <ErrorLine text={firmware.error} busy={firmware.busy} onRetry={() => firmware.run('firmware_status', {}, deadline)} />
          {firmware.result && firmware.result.status !== 'ok' && <p class="state state-error">{firmwareStatusErrorText(firmware.result)}</p>}
          {fw?.known && (
            <div class="card card-rows settings-card">
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
          {fw?.known && fw.updateAvailable && maintain && openSheet && !refusals.firmware && routerName && (
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
        </Section>
        {rebootBlock}
        {maintain && openSheet && (
          <Section title="Службы и пакеты">
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
            <button type="button" class="btn btn-ghost btn-wide" onClick={() => openSheet(restartSheet({ routerID, name: 'awgmgr', asleep }))}>
              Перезапустить awg-manager
            </button>
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
        {serviceSlot}
      </ManageGroup>

      <ManageGroup {...group('people')}>
        <Section title="Уведомления">
          <div class="card card-rows settings-card">
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
        {peopleSlot}
      </ManageGroup>

      <ManageGroup {...group('agent')}>
        {settings && (settings.role === 'owner' || settings.role === 'admin') && (() => {
          const panel = panelRow(settings)
          return (
            <Section title="Панель роутера">
              <div class="card settings-card">
                {panel.known ? (
                  <button type="button" class="panel-open" onClick={() => openExternal(panel.url)}>
                    <span class="panel-open-host">{panel.host}</span>
                    <span class="panel-open-go">открыть ↗</span>
                  </button>
                ) : (
                  <DataRow title="Панель роутера" value="адрес не сохранён" valueTone="muted" />
                )}
              </div>
              {panel.known ? (
                <p class="hint">
                  {panel.hint ? `Адрес частный: ${panel.hint}. ` : ''}Панель спросит свой логин и пароль — мы их не знаем и не храним.
                </p>
              ) : (
                <p class="hint">{panel.hint}</p>
              )}
            </Section>
          )
        })()}

        {settings && (
          <Section title="Опрос и тревоги">
            <div class="card card-rows">
              {thresholdRows(settings).map((r) => (
                <DataRow key={r.key} title={r.title} code={r.code} value={r.value} />
              ))}
              <HooksRow routerID={routerID} />
              <p class="card-foot">
                {isAdmin
                  ? 'Эти числа живут в настройках сервера и меняются там, в приложении их не поменять. Здесь они показаны, чтобы было видно, через сколько придёт тревога.'
                  : 'Эти числа меняет администратор. Здесь они показаны, чтобы было видно, через сколько придёт тревога.'}
              </p>
            </div>
          </Section>
        )}
        {agentSlot}
      </ManageGroup>

      {dangerSlot && <ManageGroup {...group('danger')}>{dangerSlot}</ManageGroup>}

      <Section title="Что умеет приложение">
        <div class="card card-rows">
          <p class="card-foot">
            <b>Роутер</b> — работает ли обход прямо сейчас и что с ним не так; при тревоге здесь же «Починить».{' '}
            <b>VPN-туннели</b> — какой VPN-туннель несёт трафик, кто подхватит, «Новый VPN-туннель» и маршруты.{' '}
            <b>Проверки</b> — спросить роутер заново, адрес выхода, проверка связи, осмотр изнутри; «Что было» — происшествия за неделю.{' '}
            <b>Настройки</b> — обслуживание и перезагрузка, уведомления и доступ, панель роутера и агент.
            {isAdmin && (
              <>
                {' '}<b>Парк</b> — все роутеры сразу: обновления агентов, новый роутер, свои серверы и бэкенд.
              </>
            )}
          </p>
          <p class="card-foot">
            Уведомления остаются у бота: приложение не может разбудить того, кто его не открыл.
            Тревога придёт в чат, а из неё кнопка ведёт сюда.
          </p>
        </div>
      </Section>
    </>
  )
}
