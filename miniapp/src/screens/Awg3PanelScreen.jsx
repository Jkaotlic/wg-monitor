import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchAwg3Peers, issueAwg3Device, issueAwg3ToRouter } from '../api.js'
import { localSheet } from '../sheet.js'
import { waitCommand, waitDeadlineMs, commandOutcome } from '../commandWait.js'
import { isStale } from '../staleness.js'
import {
  AWG3_TEXTS,
  errorBanner,
  awg3ErrorText,
  peerRows,
  summaryText,
  deviceNameProblem,
  routerPickRows,
  dmText,
} from '../awg3Panel.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { SegmentTabs } from '../ui/SegmentTabs.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { Pill } from '../ui/Pill.jsx'
import { Quoted } from '../ui/Q.jsx'
import { TextField } from '../ui/FormField.jsx'

// Экран awg3-панели: интерфейсы вкладками, сводка, пиры и два выпуска.
// Панель спрашивается только здесь -- при открытии, переключении интерфейса,
// «Повторить» и после выпуска; сервер склеивает запросы и держит ответ 30 с.
// QR -- только в состоянии экрана (data:-адрес): не в навигации, не в
// хранилище браузера, уходит вместе с экраном.
export function Awg3PanelScreen({ panelId, routers = [], backLabel = 'Свои серверы', openSheet, onClose, onEdit }) {
  const [page, setPage] = useState(null)
  const [banner, setBanner] = useState(null)
  const [loading, setLoading] = useState(true)
  const [readonly, setReadonly] = useState(false)
  const [mode, setMode] = useState('')
  const [deviceName, setDeviceName] = useState('')
  const [deviceBusy, setDeviceBusy] = useState(false)
  const [deviceError, setDeviceError] = useState('')
  const [qr, setQr] = useState(null)
  const [routerOutcome, setRouterOutcome] = useState(null)
  const alive = useRef(true)
  const busy = useRef(false)
  const seq = useRef(0)

  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  function load(iface = '') {
    const my = ++seq.current
    setLoading(true)
    fetchAwg3Peers(panelId, iface)
      .then((resp) => {
        if (!alive.current || my !== seq.current) return
        setPage(resp)
        setBanner(null)
        if (resp?.panel?.readonly === true) setReadonly(true)
      })
      .catch((err) => {
        if (!alive.current || my !== seq.current) return
        setPage(null)
        setBanner(errorBanner(err))
      })
      .finally(() => {
        if (alive.current && my === seq.current) setLoading(false)
      })
  }

  useEffect(() => {
    load('')
  }, [panelId])

  const iface = page?.iface ?? ''

  function switchIface(id) {
    if (id === iface) return
    setMode('')
    setQr(null)
    setRouterOutcome(null)
    load(id)
  }

  function toggleMode(next) {
    setMode((cur) => (cur === next ? '' : next))
    setDeviceError('')
    setRouterOutcome(null)
  }

  function issueDevice(e) {
    e?.preventDefault()
    // Замок до перерисовки: второе нажатие в том же кадре не выпускает второго пира.
    if (busy.current) return
    const problem = deviceNameProblem(deviceName)
    if (problem) {
      setDeviceError(problem)
      return
    }
    busy.current = true
    setDeviceBusy(true)
    setDeviceError('')
    setQr(null)
    issueAwg3Device(panelId, iface, deviceName.trim())
      .then((resp) => {
        if (!alive.current) return
        setQr({ src: `data:image/png;base64,${resp.qr_png_base64}`, name: resp.name, dm: dmText(resp.dm) })
        setDeviceName('')
        setMode('')
        load(iface)
      })
      .catch((err) => {
        if (!alive.current) return
        if (err?.code === 'awg3_readonly') {
          setReadonly(true)
          setMode('')
        }
        setDeviceError(awg3ErrorText(err))
      })
      .finally(() => {
        busy.current = false
        if (alive.current) setDeviceBusy(false)
      })
  }

  async function followRouter(row, resp) {
    if (!alive.current || !resp?.cmd_id) return
    setMode('')
    setRouterOutcome({ tone: 'warn', text: AWG3_TEXTS.routerWaiting })
    const router = routers.find((r) => r.id === row.id)
    let res = null
    try {
      res = await waitCommand(row.id, resp.cmd_id, { deadlineMs: waitDeadlineMs(isStale(router)), alive: () => alive.current })
    } catch {
      res = null
    }
    if (!alive.current) return
    setRouterOutcome(
      commandOutcome(res, {
        ok: `Конфиг встал на «${row.title}» VPN-туннелем «${resp.tunnel_name}».`,
        fail: 'Роутер не принял конфиг',
        pending: 'Конфиг выпущен, но роутер пока не подтвердил импорт. Загляните в его VPN-туннели позже.',
      }),
    )
    load(iface)
  }

  function pickRouter(row) {
    openSheet(
      localSheet({
        title: `Выпустить на «${row.title}»?`,
        body: AWG3_TEXTS.routerHint,
        buttonLabel: 'Выпустить',
        busyLabel: 'Выпускаем…',
        errorText: (err) => {
          if (err?.code === 'awg3_readonly' && alive.current) setReadonly(true)
          return awg3ErrorText(err)
        },
        perform: () => issueAwg3ToRouter(row.id, panelId, iface),
        onDone: (resp) => followRouter(row, resp),
      }),
    )
  }

  const title = page?.panel ? `Панель «${page.panel.label || page.panel.id}»` : 'Панель'
  const rows = peerRows(page?.peers)
  const tabs = (page?.ifaces ?? []).map((i) => ({ id: i.id, title: i.title || i.id }))
  const pick = routerPickRows(routers, page?.peers)

  return (
    <Overlay title={title} backLabel={backLabel} onBack={onClose}>
      <div class="screen awg3-panel">
        <h1 class="screen-title">
          <Quoted text={title} />
        </h1>
        {banner ? (
          <div class="card awg3-banner" role="alert">
            <p>
              <Quoted text={banner.text} />
            </p>
            {banner.retry && (
              <button type="button" class="btn btn-ghost" onClick={() => load(iface)}>
                {AWG3_TEXTS.retry}
              </button>
            )}
          </div>
        ) : !page ? (
          <p class="state">{AWG3_TEXTS.peersLoading}</p>
        ) : (
          <>
            {tabs.length > 1 && <SegmentTabs label="Интерфейсы" tabs={tabs} value={iface} onChange={switchIface} />}
            {tabs.length === 0 ? (
              <p class="state">{AWG3_TEXTS.noIfaces}</p>
            ) : (
              <Section title={`Пиры · ${summaryText(page.summary)}`}>
                {/* Правка 2 (ревью раунд 1): что значит время -- сказано ОДИН
                    раз здесь, а не словом «handshake» на каждой строке. */}
                {rows.length > 0 && <p class="hint">{AWG3_TEXTS.peersHint}</p>}
                {loading && <p class="state">{AWG3_TEXTS.peersLoading}</p>}
                {rows.length === 0 ? (
                  <p class="state">{AWG3_TEXTS.noPeers}</p>
                ) : (
                  <div class="card card-rows awg3-peers">
                    {rows.map((r) => (
                      <DataRow
                        key={r.id}
                        dot={r.dot}
                        title={r.title}
                        titleExtra={
                          r.router ? (
                            <p class="awg3-peer-tag">
                              {/* Ревью раунд 2: пилюля не резиновая -- в узкой
                                  колонке обрезаем текст многоточием и несём
                                  полную фразу в title (long-press/tooltip).
                                  Ревью раунд 3 (finding 2): полная фраза
                                  «роутер «nick»» в САМОМ тексте всё ещё
                                  вылезала за колонку на 360 px -- в тексте
                                  только ник, слово «роутер» и ёлочки живут
                                  в title. */}
                              <Pill tone="sig" title={`роутер «${r.router.nickname}»`}>
                                <span class="pill-text">{r.router.nickname}</span>
                              </Pill>
                            </p>
                          ) : r.off ? (
                            <p class="awg3-peer-tag hint">выключен</p>
                          ) : null
                        }
                        value={r.value}
                        valueSub={r.valueSub}
                      />
                    ))}
                  </div>
                )}
              </Section>
            )}
            {readonly ? (
              <p class="hint awg3-readonly">{AWG3_TEXTS.readonly}</p>
            ) : (
              iface && (
                <Section>
                  <div class="awg3-actions settings-actions">
                    <button type="button" class={`btn ${mode === 'device' ? 'btn-primary' : 'btn-ghost'}`} onClick={() => toggleMode('device')}>
                      {AWG3_TEXTS.device}
                    </button>
                    <button type="button" class={`btn ${mode === 'router' ? 'btn-primary' : 'btn-ghost'}`} onClick={() => toggleMode('router')}>
                      {AWG3_TEXTS.router}
                    </button>
                  </div>
                  {mode === 'device' && (
                    <form class="card form-group awg3-device" onSubmit={issueDevice} autocomplete="off" noValidate>
                      <p class="field-hint">{AWG3_TEXTS.deviceHint}</p>
                      <TextField
                        id="a3-device-name"
                        label={AWG3_TEXTS.deviceName}
                        value={deviceName}
                        placeholder={AWG3_TEXTS.devicePlaceholder}
                        error={deviceError}
                        onInput={(v) => {
                          setDeviceName(v)
                          setDeviceError('')
                        }}
                      />
                      <button type="submit" class="btn btn-primary btn-wide" disabled={deviceBusy}>
                        {deviceBusy ? AWG3_TEXTS.deviceBusy : AWG3_TEXTS.deviceIssue}
                      </button>
                    </form>
                  )}
                  {mode === 'router' && (
                    <>
                      <p class="field-hint">{AWG3_TEXTS.routerHint}</p>
                      {pick.length === 0 ? (
                        <p class="state">{AWG3_TEXTS.routerNone}</p>
                      ) : (
                        <ul class="card list-reset settings-card awg3-routers" aria-label={AWG3_TEXTS.routerPick}>
                          {pick.map((r) => (
                            <ListRow key={r.id} title={r.title} sub={r.sub} onClick={() => pickRouter(r)} />
                          ))}
                        </ul>
                      )}
                    </>
                  )}
                </Section>
              )
            )}
            {routerOutcome && (
              <p class={`state awg3-outcome awg3-outcome-${routerOutcome.tone}`} role="status">
                <Quoted text={routerOutcome.text} />
              </p>
            )}
          </>
        )}
        {/* Ревью раунд 3 (finding 4): QR -- ВНЕ ветки banner/page. После
            успешного выпуска экран сам перечитывает страницу (load(iface));
            если этот автоповтор упадёт, банер раньше подменял всю ветку
            выше целиком, и единственная копия QR пропадала с экрана. */}
        {qr && (
          <Section title={`QR «${qr.name}»`}>
            <div class="card awg3-qr-card">
              <img class="awg3-qr" src={qr.src} alt={AWG3_TEXTS.qrAlt} />
              <p class="field-hint">{AWG3_TEXTS.qrNote}</p>
              <p class="hint" role="status">
                {qr.dm}
              </p>
            </div>
          </Section>
        )}
        {onEdit && (
          <button type="button" class={`btn ${banner?.fix ? 'btn-primary' : 'btn-ghost'} btn-wide awg3-settings`} onClick={() => onEdit(panelId)}>
            {AWG3_TEXTS.settings}
          </button>
        )}
      </div>
    </Overlay>
  )
}
