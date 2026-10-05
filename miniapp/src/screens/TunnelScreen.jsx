import { useEffect, useRef, useState } from 'preact/hooks'
import { deleteTunnel, getAutorepair, putAutorepair } from '../api.js'
import { errorText } from '../errorText.js'
import { autorepairRow, backupFor, enableBody, enableFields, enableReady, enableSheetText } from '../autorepair.js'
import { waitCommand, waitDeadlineMs, repeatWhilePending } from '../commandWait.js'
import { localSheet, confirmSheet } from '../sheet.js'
import {
  tunnelCard,
  deleteBlock,
  deleteSheetText,
  deleteRefusal,
  deleteErrorText,
  deleteOutcome,
  tunnelAbsence,
  refusalKey,
  mayManageTunnels,
  TUNNEL_TEXTS,
} from '../tunnelDelete.js'
import { tunnelRuleSummary } from '../routes.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'
import { ExitRow } from './SignalSections.jsx'

// Экран одного VPN-туннеля: что это, сколько через него идёт и можно ли его
// удалить. Локальный слой вкладки (как мастер замены): в адрес не пишется.
//
// Удаление необратимо, поэтому подтверждается набором имени, а сервер сам
// проверяет правила и главный выход по свежему снимку. Экран не предлагает
// кнопку, которая заведомо получит отказ, и говорит причину теми же словами.
export function TunnelScreen({ routerID, asleep, snapshot, tunnelID, role, openSheet, onClose, onChanged, onOpenRebind, onRestart, canReplace = false, onReplace }) {
  const fresh = tunnelCard(snapshot, tunnelID)
  // После удаления снимок уже не знает VPN-туннель: экран держит последнее,
  // что видел, чтобы договорить итог.
  const last = useRef(fresh)
  if (fresh) last.current = fresh
  const card = fresh ?? last.current
  const [storedOutcome, setOutcome] = useState(null)
  const alive = useRef(true)
  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  // Автопочинка (v0.54): настройка этого VPN-туннеля. Строку видит только тот,
  // кому можно править (can_edit); сбой чтения -- строки нет, а не догадка.
  const [ar, setAr] = useState(null)
  const [arBusy, setArBusy] = useState(false)
  const [arError, setArError] = useState('')
  useEffect(() => {
    let live = true
    setAr(null)
    if (tunnelID) {
      getAutorepair(routerID, tunnelID)
        .then((r) => {
          if (live) setAr(r)
        })
        .catch(() => {})
    }
    return () => {
      live = false
    }
  }, [routerID, tunnelID])

  function askAutorepair() {
    const target = card
    const backup = backupFor(snapshot, target.id)
    const text = enableSheetText(ar, target.name, backup.known ? backup.name : null)
    openSheet(
      localSheet({
        title: text.title,
        body: (
          <>
            {text.sections.map((sec) => (
              <span class="sheet-sec" key={sec.h}>
                <b class="sheet-sec-h">{sec.h}</b>
                <Quoted text={sec.text} />
              </span>
            ))}
          </>
        ),
        note: text.note,
        buttonLabel: 'Включить',
        busyLabel: 'Включаем…',
        fields: enableFields(ar),
        fieldsReady: enableReady,
        errorText: (err) => (err?.serverMessage ? err.serverMessage : errorText(err)),
        perform: (_typed, values) => putAutorepair(routerID, target.id, enableBody(values)),
        onDone: (resp) => {
          if (alive.current && resp) setAr(resp)
        },
      }),
    )
  }

  // Выключение -- сразу, без листа; провайдер и вариант сервер оставляет, и
  // повторное включение предложит прежний выбор.
  async function disableAutorepair() {
    setArBusy(true)
    setArError('')
    try {
      const resp = await putAutorepair(routerID, card.id, { enabled: false, provider: ar.provider, option: ar.option, allow_relocate: ar.allow_relocate })
      if (alive.current) setAr(resp)
    } catch (e) {
      if (alive.current) setArError(errorText(e) || 'Не получилось выключить автопочинку. Попробуйте ещё раз.')
    } finally {
      if (alive.current) setArBusy(false)
    }
  }

  // Отказ сервера держится, пока снимок тот же: поменялись правила, главный
  // выход или цепочка -- экран снова решает по свежему снимку.
  const currentKey = refusalKey(fresh, snapshot)
  const outcome = storedOutcome?.kind && fresh && storedOutcome.key !== currentKey ? null : storedOutcome

  const manage = mayManageTunnels(role) && typeof openSheet === 'function'
  const block = deleteBlock(fresh)
  const refusalKind = outcome?.kind ?? ''

  const refusalKeyRef = useRef('')
  refusalKeyRef.current = currentKey

  function askDelete() {
    const target = card
    const text = deleteSheetText(target)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        note: text.note,
        buttonLabel: 'Удалить',
        busyLabel: 'Удаляем…',
        danger: true,
        // Сервер сверяет набранное как ник роутера (регистр, виды дефиса,
        // пробелы по краям) -- лист так же, чтобы не спорить с ним.
        confirmPhrase: target.name,
        confirmStrict: false,
        errorText: deleteErrorText,
        perform: async (typed) => {
          let sent
          try {
            // Сервер ждёт свежий снимок роутера несколько секунд и, не
            // дождавшись, отвечает state:"checking" -- тот же запрос
            // повторяется; второй команды роутеру повтор не ставит.
            const out = await repeatWhilePending(() => deleteTunnel(routerID, target.id, typed), {
              pending: (r) => r?.state === 'checking',
              alive: () => alive.current,
            })
            if (!alive.current) return null
            if (!out.settled || !out.resp?.cmd_id) throw Object.assign(new Error('checking_timeout'), { code: 'checking_timeout' })
            sent = out.resp
          } catch (err) {
            // Отказ по снимку -- не ошибка листа: лист закрывается, а экран
            // говорит причину и предлагает перенос.
            const refusal = deleteRefusal(err, target)
            if (refusal) return { refusal }
            throw err
          }
          // Команда уже в очереди: сорвавшееся ожидание -- не «ничего не
          // удалено», а «роутер пока не ответил».
          let result = null
          try {
            result = await waitCommand(routerID, sent.cmd_id, {
              deadlineMs: waitDeadlineMs(asleep || sent.router_asleep === true),
              alive: () => alive.current,
            })
          } catch {
            result = null
          }
          return { result }
        },
        onDone: (resp) => {
          if (!alive.current || !resp) return
          if (resp.refusal) {
            setOutcome({ tone: 'error', text: resp.refusal.text, kind: resp.refusal.kind, done: false, key: refusalKeyRef.current })
            return
          }
          setOutcome(deleteOutcome(resp.result, target.name))
          onChanged?.()
        },
      }),
    )
  }

  if (!card) {
    const foreign = tunnelAbsence(snapshot, tunnelID) === 'foreign'
    return (
      <Overlay title="VPN-туннель" backLabel="VPN-туннели" onBack={onClose}>
        <div class="screen tunnel-screen">
          <p class="state">{foreign ? TUNNEL_TEXTS.notManaged : TUNNEL_TEXTS.gone}</p>
        </div>
      </Overlay>
    )
  }

  const toRoutes = onOpenRebind ? (
    <button type="button" class="btn btn-ghost btn-wide" onClick={() => onOpenRebind(card.id)}>
      {TUNNEL_TEXTS.toRoutes}
    </button>
  ) : null

  // Замена конфига -- у работающего VPN-туннеля (смысл операции -- заменить
  // то, чем сейчас ходит трафик, не потеряв прежний). Раньше -- строка
  // вкладки; v0.52: дом действия -- экран самого VPN-туннеля.
  const replaceBtn = canReplace && onReplace ? (
    <button type="button" class="btn btn-ghost btn-wide" onClick={onReplace}>
      Заменить конфиг
    </button>
  ) : null

  // Перезапуск здорового VPN-туннеля: дом действия -- этот экран (карточка
  // тревоги держит его только на время инцидента). Без ролевого условия: у
  // прежних «Быстрых действий» его не было, и оператор не должен его терять
  // (удаление и прочее управление -- по-прежнему mayManageTunnels). Не лаймовая: главная кнопка
  // экрана -- своя или никакая.
  const restartBtn =
    typeof openSheet === 'function' && fresh ? (
      <button
        type="button"
        class="btn btn-ghost btn-wide tunnel-restart"
        onClick={() =>
          openSheet(
            confirmSheet({
              routerID,
              title: `Перезапустить «${card.name}»?`,
              body: 'Роутер опустит и снова поднимет VPN-туннель. Связь через него на несколько секунд прервётся.',
              action: 'tunnel_restart',
              args: { tunnel_id: card.id },
              buttonLabel: 'Перезапустить',
              asleep,
              onDone: onRestart ?? onChanged,
            }),
          )
        }
      >
        Перезапустить VPN-туннель
      </button>
    ) : null

  const meta = (snapshot?.tunnels ?? []).find((x) => x.id === card?.id)
  const tunnelRunning = meta ? Boolean(meta.enabled) && (!meta.status || meta.status === 'running') : true

  return (
    <Overlay title="VPN-туннель" backLabel="VPN-туннели" onBack={onClose}>
      <div class="screen tunnel-screen">
        <h1 class="screen-title">
          <Quoted text={`«${card.name}»`} />
        </h1>
        <div class="card card-rows">
          <DataRow title="Состояние" code={card.id} value={fresh ? card.stateLabel : outcome?.done ? 'удалён с роутера' : 'нет в снимке роутера'} />
          {/* Пропавший из снимка VPN-туннель: прежние интерфейс и правила
              были бы вчерашней картиной. */}
          {fresh && <DataRow title="Интерфейс" value={card.iface || 'роутер не сообщил'} />}
          {fresh && <DataRow title="Правила" value={tunnelRuleSummary(card)} />}
          {fresh && <ExitRow routerID={routerID} tunnelID={card.id} running={tunnelRunning} />}
          {fresh && card.egressKnown && <DataRow title="Главный выход роутера" value={card.isDefault ? 'этот VPN-туннель' : 'другой'} />}
        </div>

        {fresh && ar?.can_edit && typeof openSheet === 'function' && (() => {
          const row = autorepairRow(ar)
          return (
            <>
              <div class="card card-rows">
                <DataRow title={row.title} value={row.value} />
              </div>
              <p class="hint">{row.hint}</p>
              <button type="button" class="btn btn-ghost btn-wide tunnel-autorepair" disabled={arBusy} onClick={ar.enabled ? disableAutorepair : askAutorepair}>
                {ar.enabled ? 'Выключить автопочинку' : 'Включить автопочинку'}
              </button>
              {arError && <p class="state state-error" role="status">{arError}</p>}
            </>
          )
        })()}

        {replaceBtn}
        {restartBtn}

        <Section title="Удалить VPN-туннель">
          {/* Роль ещё не пришла -- молчать: слова о правах были бы догадкой. */}
          {!manage && role && <p class="hint">{TUNNEL_TEXTS.manageOnly}</p>}
          {manage && block && !outcome && (
            <>
              <div class="card tunnel-block">
                <p class="traffic-detail">
                  <Quoted text={block.text} />
                </p>
              </div>
              {block.kind === 'rules' && toRoutes}
            </>
          )}
          {/* Команда ушла, а ответа нет (tone warn) -- второй раз не предлагать. */}
          {manage && fresh && !block && !outcome?.done && !refusalKind && outcome?.tone !== 'warn' && (
            <>
              <p class="hint">{TUNNEL_TEXTS.deleteHint}</p>
              <button type="button" class="btn btn-danger btn-wide tunnel-delete" onClick={askDelete}>
                Удалить VPN-туннель
              </button>
            </>
          )}
          {outcome && (
            <p class={`state tunnel-outcome${outcome.tone === 'error' ? ' state-error' : ''}`} role="status">
              <Quoted text={outcome.text} />
            </p>
          )}
          {(refusalKind === 'rules' || refusalKind === 'chain') && toRoutes}
          {outcome?.done && (
            <button type="button" class="btn btn-primary btn-wide" onClick={onClose}>
              К списку VPN-туннелей
            </button>
          )}
        </Section>
      </div>
    </Overlay>
  )
}
