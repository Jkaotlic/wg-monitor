import { useEffect, useRef, useState } from 'preact/hooks'
import { deleteTunnel } from '../api.js'
import { waitCommand, waitDeadlineMs, repeatWhilePending } from '../commandWait.js'
import { localSheet } from '../sheet.js'
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
import { tunnelLiveLabel } from '../labels.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'

// Экран одного VPN-туннеля: что это, сколько через него идёт и можно ли его
// удалить. Локальный слой вкладки (как мастер замены): в адрес не пишется.
//
// Удаление необратимо, поэтому подтверждается набором имени, а сервер сам
// проверяет правила и главный выход по свежему снимку. Экран не предлагает
// кнопку, которая заведомо получит отказ, и говорит причину теми же словами.
export function TunnelScreen({ routerID, asleep, snapshot, tunnelID, role, openSheet, onClose, onChanged, onOpenRebind }) {
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

  return (
    <Overlay title="VPN-туннель" backLabel="VPN-туннели" onBack={onClose}>
      <div class="screen tunnel-screen">
        <h1 class="screen-title">
          <Quoted text={`«${card.name}»`} />
        </h1>
        <div class="card">
          <DataRow title="Состояние" code={card.id} value={fresh ? tunnelLiveLabel(card.live) : 'нет в снимке роутера'} />
          <DataRow title="Интерфейс" value={card.iface || 'роутер не сообщил'} />
          <DataRow title="Правила" value={tunnelRuleSummary(card)} />
          {card.egressKnown && <DataRow title="Главный выход роутера" value={card.isDefault ? 'этот VPN-туннель' : 'другой'} />}
        </div>

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
