import { useEffect, useState } from 'preact/hooks'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'
import {
  fetchFleet,
  createWebLink,
  updateRouterAgent,
  cancelRouterAgentUpdate,
  updateFleetAgents,
} from '../api.js'
import { openExternal } from '../telegram.js'
import { localSheet } from '../sheet.js'
import {
  backendRow,
  fleetHeadline,
  fleetRouterRows,
  notifyGapLines,
  watchdogLine,
  webLinkLines,
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

// «Парк» -- админский экран всего парка: состояние, версии и обслуживание
// агентов. Раньше экран был читающим, а обновление агента жило в боте и
// дашборде; теперь кнопки здесь, и каждая либо делает дело, либо её нет.
//
// Решение оператора: «три необновлённых роутера выключены — нужно иметь
// возможность обновлять … в пендинг». Поэтому «Обновить агент» у выключенного
// роутера не гаснет: намерение ставится на сервере и доживает до включения.
//
// Парк видит только админ: сервер отвечает 404 всем остальным, и этот признак
// в клиенте -- подсказка интерфейсу, а не граница доступа.
export function ParkSection({ openSheet }) {
  const [fleet, setFleet] = useState(null)
  const [fleetError, setFleetError] = useState(null)
  // Итог последнего действия с одним роутером -- одна строка над списком:
  // лист к этому моменту уже закрыт, а человек должен увидеть, чем кончилось.
  const [notice, setNotice] = useState('')
  const [fleetResult, setFleetResult] = useState(null)

  const [linkBusy, setLinkBusy] = useState(false)
  const [linkLines, setLinkLines] = useState([])
  const [linkError, setLinkError] = useState(null)

  function load() {
    return fetchFleet()
      .then((data) => {
        setFleet(data)
        setFleetError(null)
      })
      .catch(() => setFleetError('Не удалось прочитать сводку парка.'))
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
        setLinkLines(webLinkLines(grant))
        openExternal(grant.url)
      })
      .catch((err) => setLinkError(err?.serverMessage || 'Не удалось выдать ссылку.'))
      .finally(() => setLinkBusy(false))
  }

  function askUpdate(router) {
    const text = agentUpdateSheetText(router, fleet?.backend?.version ?? '')
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Обновить',
        confirmPhrase: router.nickname,
        errorText: agentUpdateErrorText,
        // Набранное имя уходит серверу: он сверяет его сам, проверка на листе --
        // только пауза для человека. Цель версии не отправляем: по умолчанию
        // сервер ставит свою собственную.
        perform: (typed) => updateRouterAgent(router.id, typed),
        onDone: (resp) => {
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
          setNotice(agentCancelDoneText(resp, router.nickname))
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
        confirmPhrase: FLEET_UPDATE_PHRASE,
        errorText: fleetUpdateErrorText,
        perform: (typed) => updateFleetAgents(typed),
        onDone: (resp) => {
          setFleetResult(fleetUpdateSummary(resp?.results))
          load()
        },
      }),
    )
  }

  const rows = fleet ? fleetRouterRows(fleet) : []
  const gaps = fleet ? notifyGapLines(fleet) : []
  const watchdog = fleet ? watchdogLine(fleet) : ''
  const backend = fleet ? backendRow(fleet) : null
  const behind = fleet ? fleetUpdateTargets(fleet).length : 0

  return (
    <Section title="Парк">
      {fleetError ? (
        <p class="state state-error">{fleetError}</p>
      ) : !fleet ? (
        <p class="state">Загрузка…</p>
      ) : (
        <>
          <p class="router-lastseen">{fleetHeadline(fleet)}</p>

          <div class="card">
            <DataRow title="Бэкенд" value={backend.value} valueSub={backend.sub} />
          </div>

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
                <p class="hint" key={line}>
                  <Quoted text={line} />
                </p>
              ))}
            </div>
          )}
          {notice && (
            <p class="hint">
              <Quoted text={notice} />
            </p>
          )}

          {rows.length > 0 && (
            <div class="card">
              {rows.map((row) => (
                <div class="park-row" key={row.id}>
                  <DataRow title={row.name} value={row.state} valueSub={row.sub} />
                  {(row.versions || row.hint) && (
                    <p class="hint">{[row.versions, row.hint].filter(Boolean).join(' · ')}</p>
                  )}
                  {row.update.text && (
                    <p class={`park-update park-update-${row.update.tone}`}>
                      <Quoted text={row.update.text} />
                    </p>
                  )}
                  {row.warning && row.update.canUpdate && <p class="hint">Оговорка: {row.warning}</p>}
                  {(row.update.canUpdate || row.update.canCancel) && (
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
                    </div>
                  )}
                </div>
              ))}
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

          {watchdog && <p class="hint">Сторож парка: {watchdog}</p>}

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
    </Section>
  )
}
