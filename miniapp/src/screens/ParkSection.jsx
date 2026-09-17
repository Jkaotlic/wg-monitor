import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'
import {
  fetchFleet,
  createWebLink,
  updateRouterAgent,
  cancelRouterAgentUpdate,
  reviveRouterAgent,
  cancelRouterAgentRevive,
  updateFleetAgents,
  sendCommand,
  fetchCommandResult,
  setRouterNotify,
  deployBackend,
} from '../api.js'
import { openExternal } from '../telegram.js'
import { localSheet } from '../sheet.js'
import { AppContext } from '../appContext.js'
import {
  backendRow,
  fleetHeadline,
  fleetRouterRows,
  notifyGapLines,
  notifyMuteSheetText,
  webLinkLines,
  withNotifyMuted,
} from '../fleetAdmin.js'
import {
  FLEET_UPDATE_PHRASE,
  agentUpdateSheetText,
  agentUpdateErrorText,
  agentUpdateDoneText,
  agentCancelDoneText,
  fleetUpdateTargets,
  fleetUpdateSheetText,
  fleetUpdateSummary,
  fleetUpdateErrorText,
} from '../agentUpdate.js'
import {
  REVIVE_SECRET_NOTE,
  reviveState,
  reviveNotConfiguredLine,
  reviveSheetText,
  reviveFields,
  reviveReady,
  reviveRequestBody,
  reviveErrorText,
  reviveDoneText,
  reviveCancelSheetText,
  reviveCancelDoneText,
} from '../revive.js'
import { BATCH, runFleetBatch, batchProgressLine, batchSummary } from '../fleetBatch.js'
import { backendDeployOffer, backendDeploySheetText, backendDeployErrorText } from '../backendDeploy.js'
import { watchdogLine, routerDelayLines } from '../watchdogLine.js'

// «Парк» -- админский экран всего парка: состояние, версии и обслуживание
// агентов. Раньше экран был читающим, а обновление агента жило в боте и
// дашборде; теперь кнопки здесь, и каждая либо делает дело, либо её нет.
//
// Решение оператора: «три необновлённых роутера выключены — нужно иметь
// возможность обновлять … в пендинг». Поэтому «Обновить агент» у выключенного
// роутера не гаснет: намерение ставится на сервере и доживает до включения.
//
// Оживление агента (цикл 2б) -- тот же случай, шаг дальше: агент на
// выключенном роутере мёртв, и сервер переустановит его сам, когда роутер
// появится. Решение оператора: «Полный автомат: пароль root или вход в
// панель awg-manager вводится один раз при постановке, хранится на Pi
// зашифрованным до успеха, отмены или срока, затем стирается.» Пароль
// вводится на листе и в состояние экрана не попадает (Sheet.jsx).
//
// Парк видит только админ: сервер отвечает 404 всем остальным, и этот признак
// в клиенте -- подсказка интерфейсу, а не граница доступа.
export function ParkSection({ openSheet, onOpenRouter, currentID, openLayer }) {
  const { mode } = useContext(AppContext)
  const [fleet, setFleet] = useState(null)
  const [fleetError, setFleetError] = useState(null)
  // Итог последнего действия с одним роутером -- одна строка над списком:
  // лист к этому моменту уже закрыт, а человек должен увидеть, чем кончилось.
  const [notice, setNotice] = useState('')
  const [fleetResult, setFleetResult] = useState(null)

  // Массовая проверка -- одна на экран: две одновременно смешали бы счёт
  // ответивших, а роутеру пришли бы две команды подряд.
  const [batch, setBatch] = useState(null)

  // По роутеру (Map), не одним значением на экран: переключение одной строки
  // не должно гасить занятость или ошибку другой, чей запрос к серверу ещё
  // не вернулся.
  const [notifyBusy, setNotifyBusy] = useState(() => new Set())
  const [notifyError, setNotifyError] = useState(() => new Map())

  const [linkBusy, setLinkBusy] = useState(false)
  const [linkLines, setLinkLines] = useState([])
  const [linkError, setLinkError] = useState(null)

  // aliveRef -- экран мог уйти, пока fetchFleet ещё в пути: setState на
  // размонтированном экране здесь не ошибка (Preact не ругается, в отличие
  // от React), но и обещать честной не становится -- проверяем перед каждым
  // применением ответа.
  const aliveRef = useRef(true)
  // Синхронный флаг занятости пачки: batch?.running -- state, он меняется
  // только на следующем кадре, и второй тап до перерисовки прошёл бы мимо
  // проверки. batchCancelRef.current.cancelled рвёт цикл при уходе с экрана
  // -- runFleetBatch видит его в while-опросе и в очереди пула.
  const batchRunningRef = useRef(false)
  const batchCancelRef = useRef({ cancelled: false })

  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
      batchCancelRef.current.cancelled = true
    }
  }, [])

  function load() {
    return fetchFleet()
      .then((data) => {
        if (!aliveRef.current) return
        setFleet(data)
        setFleetError(null)
      })
      .catch(() => {
        if (aliveRef.current) setFleetError('Не удалось прочитать сводку парка.')
      })
  }

  useEffect(() => {
    load()
  }, [])

  // Ссылку выдаёт сервер, он же говорит словами про срок и лимит: своих
  // текстов про «12 часов» приложение не сочиняет.
  function openInBrowser() {
    setLinkBusy(true)
    setLinkError(null)
    createWebLink()
      .then((grant) => {
        // Не только setState: открывать ссылку в браузере на экране, который
        // уже покинули, тоже не дело -- побочный эффект, не только строка.
        if (!aliveRef.current) return
        setLinkLines(webLinkLines(grant))
        openExternal(grant.url)
      })
      .catch((err) => {
        if (aliveRef.current) setLinkError(err?.serverMessage || 'Не удалось выдать ссылку.')
      })
      .finally(() => {
        if (aliveRef.current) setLinkBusy(false)
      })
  }

  function askUpdate(router) {
    const text = agentUpdateSheetText(router, fleet?.backend?.version ?? '')
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Обновить',
        busyLabel: 'Ставим…',
        confirmPhrase: router.nickname,
        errorText: agentUpdateErrorText,
        // Набранное имя уходит серверу: он сверяет его сам, проверка на листе --
        // только пауза для человека. Цель версии не отправляем: по умолчанию
        // сервер ставит свою собственную.
        perform: (typed) => updateRouterAgent(router.id, typed),
        onDone: (resp) => {
          setFleetResult(null)
          setNotice(agentUpdateDoneText(resp, router.nickname))
          load()
        },
      }),
    )
  }

  function askCancel(router) {
    openSheet(
      localSheet({
        title: `Отменить обновление агента на «${router.nickname}»?`,
        body: `Роутер не получит ${router.pending_version}, даже когда выйдет на связь. Поставить обновление можно будет заново.`,
        buttonLabel: 'Отменить обновление',
        perform: () => cancelRouterAgentUpdate(router.id),
        onDone: (resp) => {
          setFleetResult(null)
          setNotice(agentCancelDoneText(resp, router.nickname))
          load()
        },
      }),
    )
  }

  function askRevive(router) {
    const text = reviveSheetText(router)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Оживить',
        busyLabel: 'Ставим…',
        confirmPhrase: router.nickname,
        fields: reviveFields(router),
        fieldsReady: reviveReady(router),
        note: REVIVE_SECRET_NOTE,
        errorText: reviveErrorText,
        // values -- снимок полей листа; тело собирает revive.js, и дальше
        // этого вызова пароль в экране не живёт.
        perform: (typed, values) => reviveRouterAgent(router.id, reviveRequestBody(values, typed, router)),
        onDone: (resp) => {
          setFleetResult(null)
          setNotice(reviveDoneText(resp, router.nickname))
          load()
        },
      }),
    )
  }

  function askReviveCancel(router) {
    const text = reviveCancelSheetText(router)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Отменить оживление',
        errorText: reviveErrorText,
        perform: () => cancelRouterAgentRevive(router.id),
        onDone: (resp) => {
          setFleetResult(null)
          setNotice(reviveCancelDoneText(resp, router.nickname))
          load()
        },
      }),
    )
  }

  function askUpdateAll() {
    const text = fleetUpdateSheetText(fleet)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Обновить всех',
        busyLabel: 'Ставим…',
        confirmPhrase: FLEET_UPDATE_PHRASE,
        errorText: fleetUpdateErrorText,
        perform: (typed) => updateFleetAgents(typed),
        onDone: (resp) => {
          setNotice('')
          setFleetResult(fleetUpdateSummary(resp?.results))
          load()
        },
      }),
    )
  }

  // Раскатка бэкенда: подтверждение набором версии, дальше -- полноэкранное
  // ожидание (слой backenddeploy). Сервер перезапустится, и приложению на это
  // время некуда вернуться; отката отсюда нет.
  function askBackendDeploy() {
    const offer = backendDeployOffer(fleet)
    if (!offer || !openLayer) return
    const text = backendDeploySheetText(offer.target)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Обновить бэкенд',
        busyLabel: 'Отправляем…',
        confirmPhrase: offer.target,
        errorText: backendDeployErrorText,
        perform: (typed) => deployBackend(offer.target, typed),
        onDone: (resp) => openLayer('backenddeploy', { targetVersion: resp?.target_version || offer.target }),
      }),
    )
  }

  async function runBatch(kind) {
    if (!fleet || batchRunningRef.current) return
    batchRunningRef.current = true
    const signal = { cancelled: false }
    batchCancelRef.current = signal
    try {
      await runFleetBatch({
        kind,
        routers: fleet.routers ?? [],
        send: sendCommand,
        poll: fetchCommandResult,
        onProgress: (s) => { if (aliveRef.current) setBatch(s) },
        signal,
      })
      // Аудит обновляет снимок версий на сервере -- строки парка читают его
      // же, но только если экран ещё здесь: на размонтированном перечитывать нечего.
      if (aliveRef.current && kind === 'audit') load()
    } finally {
      batchRunningRef.current = false
    }
  }

  // Решение оператора: «отключить уведомления в личку от определённого
  // роутера, но и одновременно при желании зайти глянуть, что не так».
  // Выключение спрашивает «точно?» (как на экране настроек), включение
  // обратно -- сразу: вернуть сообщения нельзя сделать по ошибке во вред.
  function saveNotify(router, muted) {
    setNotifyBusy((prev) => new Set(prev).add(router.id))
    setNotifyError((prev) => {
      if (!prev.has(router.id)) return prev
      const next = new Map(prev)
      next.delete(router.id)
      return next
    })
    return setRouterNotify(router.id, muted)
      .then((resp) => {
        if (aliveRef.current) setFleet((prev) => withNotifyMuted(prev, router.id, resp?.muted ?? muted))
      })
      .catch((err) => {
        if (aliveRef.current) setNotifyError((prev) => new Map(prev).set(router.id, 'Не удалось сохранить. Попробуйте ещё раз.'))
        throw err
      })
      .finally(() => {
        if (aliveRef.current) {
          setNotifyBusy((prev) => {
            const next = new Set(prev)
            next.delete(router.id)
            return next
          })
        }
      })
  }

  function toggleNotify(router) {
    if (router.notify_muted) {
      saveNotify(router, false).catch(() => {})
      return
    }
    const text = notifyMuteSheetText(router)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Не уведомлять',
        perform: () => saveNotify(router, true),
      }),
    )
  }

  const rows = fleet ? fleetRouterRows(fleet) : []
  const gaps = fleet ? notifyGapLines(fleet) : []
  const watchdog = fleet ? watchdogLine(fleet) : null
  const backend = fleet ? backendRow(fleet) : null
  const deployOffer = fleet ? backendDeployOffer(fleet) : null
  const behind = fleet ? fleetUpdateTargets(fleet).length : 0
  const revives = new Map(rows.map((row) => [row.id, reviveState(row.router, fleet)]))
  const reviveOff = fleet ? reviveNotConfiguredLine(fleet) : ''

  return (
    <Section title="Парк">
      {/* Отказ первого чтения -- когда rows ещё нет вовсе, показывать нечего,
          кроме ошибки. Отказ ПОВТОРНОГО чтения (после действия) не должен
          стирать уже показанный список -- ошибка тогда идёт отдельной
          строкой рядом с ним, а не вместо него. */}
      {fleetError && !fleet ? (
        <p class="state state-error">{fleetError}</p>
      ) : !fleet ? (
        <p class="state">Загрузка…</p>
      ) : (
        <>
          <p class="router-lastseen">{fleetHeadline(fleet)}</p>
          {fleetError && <p class="state state-error">{fleetError}</p>}

          <div class="card park-backend">
            <DataRow title="Бэкенд" value={backend.value} valueSub={backend.sub} />
            {/* Сторож -- рядом с бэкендом: это его процесс, и «молчат N»
                читается как ответ на «кто сейчас не на связи», а не как
                сноска под списком. */}
            {watchdog && (
              <div class={`park-watchdog park-watchdog-${watchdog.tone}`}>
                <p class="park-watchdog-line">Сторож: {watchdog.text}</p>
                {watchdog.alarm && <p class="state state-error">{watchdog.alarm}</p>}
                {watchdog.sub && <p class="hint">{watchdog.sub}</p>}
              </div>
            )}
            {deployOffer && openLayer && (
              <div class="park-backend-actions">
                <button type="button" class="btn btn-ghost btn-row" onClick={askBackendDeploy}>
                  {deployOffer.label}
                </button>
              </div>
            )}
          </div>

          {/* Новый роутер: мастер -- слой парка, открывается с возвратом сюда. */}
          {openLayer && (
            <button type="button" class="btn btn-ghost btn-wide park-add" onClick={() => openLayer('provision')}>
              Добавить роутер
            </button>
          )}

          {behind > 0 && (
            <button type="button" class="btn btn-primary btn-wide" onClick={askUpdateAll}>
              Обновить всех отставших ({behind})
            </button>
          )}
          {fleetResult && (
            <div class="park-result">
              <p class="hint">
                <b>{fleetResult.headline}</b>
              </p>
              {fleetResult.lines.map((line) => (
                <p class="hint" key={line.id}>
                  <Quoted text={line.text} />
                </p>
              ))}
            </div>
          )}
          {notice && (
            <p class="hint">
              <Quoted text={notice} />
            </p>
          )}

          <div class="settings-actions park-batch">
            {['doctor', 'audit'].map((kind) => (
              <button
                key={kind}
                type="button"
                class="btn btn-ghost"
                disabled={Boolean(batch?.running)}
                onClick={() => runBatch(kind)}
              >
                {batch?.running && batch.kind === kind ? BATCH[kind].busy : BATCH[kind].idle}
              </button>
            ))}
          </div>
          {batch?.running ? (
            <p class="hint">{batchProgressLine(batch)}</p>
          ) : batch ? (
            (() => {
              const summary = batchSummary(batch)
              return (
                <div class="park-result">
                  <p class="hint">
                    <b>{summary.headline}</b>
                  </p>
                  {summary.lines.map((line) => (
                    <p class="hint" key={line.id}>
                      <Quoted text={line.text} />
                    </p>
                  ))}
                </div>
              )
            })()
          ) : (
            <p class="hint">
              Осмотр и сверка версий на каждом роутере на связи. Ничего не меняют; выключенные и
              спящие пропускаются.
            </p>
          )}

          {reviveOff && <p class="hint">{reviveOff}</p>}
          {rows.length > 0 && (
            <div class="card">
              {rows.map((row) => {
                const rv = revives.get(row.id)
                return (
                <div class="park-row" key={row.id}>
                  <DataRow title={row.name} value={row.state} valueSub={row.sub} />
                  <div class="park-row-controls">
                    <button
                      type="button"
                      role="switch"
                      aria-checked={row.notify.on ? 'true' : 'false'}
                      class="park-switch"
                      disabled={notifyBusy.has(row.id)}
                      onClick={() => toggleNotify(row.router)}
                    >
                      <span class="park-switch-track" aria-hidden="true">
                        <span class="park-switch-thumb" />
                      </span>
                      <span>уведомлять меня</span>
                    </button>
                    {onOpenRouter && row.id !== currentID && (
                      <button type="button" class="btn btn-ghost btn-row" onClick={() => onOpenRouter(row.id)}>
                        Открыть роутер
                      </button>
                    )}
                  </div>
                  {row.notify.note && <p class="hint">{row.notify.note}</p>}
                  {notifyError.has(row.id) && <p class="state state-error">{notifyError.get(row.id)}</p>}
                  {(row.versions || row.hint) && (
                    <p class="hint">{[row.versions, row.hint].filter(Boolean).join(' · ')}</p>
                  )}
                  {row.update.text && (
                    <p class={`park-update park-update-${row.update.tone}`}>
                      <Quoted text={row.update.text} />
                    </p>
                  )}
                  {routerDelayLines(row.router).map((line) => (
                    <p key={line.key} class={`park-update park-delay park-update-${line.tone}`}>
                      {line.text}
                    </p>
                  ))}
                  {/* Не только рядом с кнопкой «Обновить»: у слишком старого
                      агента (B6) canUpdate=false -- self_update ему
                      недоступен вовсе, но именно поэтому предупреждение
                      обязано быть видно, а не пропадать вместе с кнопкой. */}
                  {row.warning && <p class="hint">Оговорка: {row.warning}</p>}
                  {/* «оживление:» -- рядом стоит строка обновления, и одинокое
                      «ожил» или «срок истёк» читалось бы как про обновление. */}
                  {rv.text && (
                    <p class={`park-update park-update-${rv.tone}`}>оживление: {rv.text}</p>
                  )}
                  {(row.update.canUpdate || row.update.canCancel || rv.canRevive || rv.canCancel) && (
                    <div class="park-actions">
                      {row.update.canUpdate && (
                        <button type="button" class="btn btn-ghost btn-row" onClick={() => askUpdate(row.router)}>
                          Обновить агент
                        </button>
                      )}
                      {row.update.canCancel && (
                        <button type="button" class="btn btn-ghost btn-row" onClick={() => askCancel(row.router)}>
                          Отменить обновление
                        </button>
                      )}
                      {rv.canRevive && (
                        <button type="button" class="btn btn-ghost btn-row" onClick={() => askRevive(row.router)}>
                          Оживить агент
                        </button>
                      )}
                      {rv.canCancel && (
                        <button type="button" class="btn btn-ghost btn-row" onClick={() => askReviveCancel(row.router)}>
                          Отменить оживление
                        </button>
                      )}
                    </div>
                  )}
                </div>
                )
              })}
            </div>
          )}

          {gaps.length > 0 && (
            <>
              <h3 class="row-title">Уведомления</h3>
              {gaps.map((line) => (
                <p class="hint" key={line}>
                  {line}
                </p>
              ))}
            </>
          )}

          {/* В браузере личная ссылка на браузер бессмысленна -- человек уже
              здесь. Мостика в классическое веб-управление больше нет: всё,
              что там было, переехало сюда (цикл 2). */}
          {mode !== 'web' && (
            <>
              <button type="button" class="btn btn-ghost btn-wide" disabled={linkBusy} onClick={openInBrowser}>
                {linkBusy ? 'Выдаём ссылку…' : 'Открыть в браузере'}
              </button>
              {linkLines.map((line) => (
                <p class="hint" key={line}>
                  {line}
                </p>
              ))}
              {linkError && <p class="state state-error">{linkError}</p>}
            </>
          )}
        </>
      )}
    </Section>
  )
}
