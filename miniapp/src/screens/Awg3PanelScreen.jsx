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
import { Awg3Issuers } from './Awg3Issuers.jsx'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { SegmentTabs } from '../ui/SegmentTabs.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Pill } from '../ui/Pill.jsx'
import { Quoted } from '../ui/Q.jsx'

// Экран awg3-панели: интерфейсы вкладками, сводка, пиры и два выпуска.
// Панель спрашивается только здесь -- при открытии, переключении интерфейса,
// «Повторить» и после выпуска; сервер склеивает запросы и держит ответ 30 с.
// QR -- только в состоянии экрана (data:-адрес): не в навигации, не в
// хранилище браузера, уходит вместе с экраном.
export function Awg3PanelScreen({ panelId, routers = [], backLabel = 'Свои серверы', openSheet, onClose, onEdit, onOpenRouterTunnels }) {
  const [page, setPage] = useState(null)
  const [banner, setBanner] = useState(null)
  const [loading, setLoading] = useState(true)
  const [readonly, setReadonly] = useState(false)
  const [qr, setQr] = useState(null)
  const [routerOutcome, setRouterOutcome] = useState(null)
  const alive = useRef(true)
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
    setQr(null)
    setRouterOutcome(null)
    load(id)
  }

  // «Конфиг на устройство» -- листом (v0.50, спека п. 2.7): лайм только у
  // «Выпустить» внутри. Имя живёт в поле листа (keep -- переживает отказ
  // сервера). Два нажатия в одном кадре -- один запрос: второй получает тот же
  // ответ.
  function askDevice() {
    setRouterOutcome(null)
    let pending = null
    openSheet(
      localSheet({
        title: AWG3_TEXTS.device,
        body: AWG3_TEXTS.deviceHint,
        buttonLabel: AWG3_TEXTS.deviceIssue,
        busyLabel: AWG3_TEXTS.deviceBusy,
        fields: [
          {
            name: 'name',
            label: AWG3_TEXTS.deviceName,
            placeholder: AWG3_TEXTS.devicePlaceholder,
            keep: true,
            hint: (v) => (v.name ? deviceNameProblem(v.name) : ''),
          },
        ],
        fieldsReady: (v) => !deviceNameProblem(v.name),
        errorText: (err) => {
          if (err?.code === 'awg3_readonly' && alive.current) setReadonly(true)
          return awg3ErrorText(err)
        },
        perform: (_typed, values) => {
          if (!pending) {
            if (alive.current) setQr(null)
            pending = issueAwg3Device(panelId, iface, String(values.name ?? '').trim()).finally(() => {
              pending = null
            })
          }
          return pending
        },
        onDone: (resp) => {
          if (!alive.current || !resp) return
          setQr({ src: `data:image/png;base64,${resp.qr_png_base64}`, name: resp.name, dm: dmText(resp.dm) })
          load(iface)
        },
      }),
    )
  }

  // «Выпустить на роутер» -- листом с выбором роутера; после итога --
  // переход на его VPN-туннели.
  function askRouter() {
    setRouterOutcome(null)
    const pick = routerPickRows(routers, page?.peers)
    let chosen = null
    openSheet(
      localSheet({
        title: AWG3_TEXTS.router,
        body: AWG3_TEXTS.routerHint,
        buttonLabel: AWG3_TEXTS.deviceIssue,
        busyLabel: AWG3_TEXTS.deviceBusy,
        fields: [
          {
            name: 'router',
            type: 'select',
            label: AWG3_TEXTS.routerPick,
            keep: true,
            options: [{ value: '', label: 'Выберите роутер' }, ...pick.map((r) => ({ value: String(r.id), label: r.title }))],
            hint: (v) => pick.find((r) => String(r.id) === v.router)?.sub ?? '',
          },
        ],
        fieldsReady: (v) => Boolean(v.router),
        errorText: (err) => {
          if (err?.code === 'awg3_readonly' && alive.current) setReadonly(true)
          return awg3ErrorText(err)
        },
        perform: (_typed, values) => {
          chosen = pick.find((r) => String(r.id) === values.router) ?? null
          return issueAwg3ToRouter(chosen.id, panelId, iface)
        },
        onDone: (resp) => {
          if (chosen) followRouter(chosen, resp)
        },
      }),
    )
  }

  async function followRouter(row, resp) {
    if (!alive.current || !resp?.cmd_id) return
    setRouterOutcome({ tone: 'warn', text: AWG3_TEXTS.routerWaiting })
    const router = routers.find((r) => r.id === row.id)
    let res = null
    try {
      res = await waitCommand(row.id, resp.cmd_id, { deadlineMs: waitDeadlineMs(isStale(router)), alive: () => alive.current })
    } catch {
      res = null
    }
    if (!alive.current) return
    const outcome = commandOutcome(res, {
      ok: `Конфиг встал на «${row.title}» VPN-туннелем «${resp.tunnel_name}».`,
      fail: 'Роутер не принял конфиг',
      pending: 'Конфиг выпущен, но роутер пока не подтвердил импорт. Загляните в его VPN-туннели позже.',
    })
    // «Открыть VPN-туннели» -- только после успеха: при отказе или ожидании
    // открывать пока нечего.
    setRouterOutcome(outcome.tone === 'ok' ? { ...outcome, routerID: row.id, routerName: row.title } : outcome)
    load(iface)
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
              <Section title={`Устройства · ${summaryText(page.summary)}`}>
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
                  <div class="awg3-actions action-row">
                    <button type="button" class="btn btn-ghost" onClick={askDevice}>
                      {AWG3_TEXTS.device}
                    </button>
                    <button type="button" class="btn btn-ghost" disabled={pick.length === 0} onClick={askRouter}>
                      {AWG3_TEXTS.router}
                    </button>
                  </div>
                  {pick.length === 0 && <p class="hint">{AWG3_TEXTS.routerNone}</p>}
                </Section>
              )
            )}
            <Awg3Issuers panel={page.panel} onChanged={(p) => p && setPage((prev) => ({ ...prev, panel: p }))} />
            {routerOutcome && (
              <p class={`state awg3-outcome awg3-outcome-${routerOutcome.tone}`} role="status">
                <Quoted text={routerOutcome.text} />
              </p>
            )}
            {routerOutcome?.routerID != null && onOpenRouterTunnels && (
              <button type="button" class="btn btn-ghost btn-wide awg3-open-tunnels" onClick={() => onOpenRouterTunnels(routerOutcome.routerID)}>
                <Quoted text={`Открыть VPN-туннели «${routerOutcome.routerName}»`} />
              </button>
            )}
          </>
        )}
        {/* Ревью раунд 3 (finding 4): QR -- ВНЕ ветки banner/page. После
            успешного выпуска экран сам перечитывает страницу (load(iface));
            если этот автоповтор упадёт, банер раньше подменял всю ветку
            выше целиком, и единственная копия QR пропадала с экрана. */}
        {qr && (
          <section class="section">
            <h2 class="awg3-qr-title">
              <Quoted text={`QR-код «${qr.name}»`} />
            </h2>
            <div class="card awg3-qr-card">
              <img class="awg3-qr" src={qr.src} alt={AWG3_TEXTS.qrAlt} />
              <p class="field-hint">{AWG3_TEXTS.qrNote}</p>
              <p class="hint" role="status">
                {qr.dm}
              </p>
            </div>
          </section>
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
