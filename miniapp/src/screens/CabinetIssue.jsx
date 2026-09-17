import { useEffect, useRef, useState } from 'preact/hooks'
import { issueVPNConfig, fetchCommandResult, sendVPNConf } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  CABINET_TEXTS,
  issueExplain,
  issueArgs,
  issueFailure,
  sendConfSheetText,
  sendConfErrorText,
  SEND_CONF_DONE,
} from '../cabinetKeys.js'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'

// «Что произойдёт»: выпуск выбранного варианта и импорт на роутер. Конфиг
// скачивает сервер и сам кладёт его в команду агенту -- через приложение он
// не проходит. «Прислать .conf в личку» (админ и владелец) -- файл
// отправляет бот лично нажавшему; на листе сказано, что в файле приватный ключ.
export function CabinetIssue({ routerID, asleep, pending, perms, openSheet, onIssued, onBusy, onBackToList }) {
  const [phase, setPhase] = useState('idle')
  const [outcome, setOutcome] = useState('')
  const [offerRevoke, setOfferRevoke] = useState(false)
  const [sendNotice, setSendNotice] = useState('')
  const alive = useRef(true)
  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )
  // Экран кабинета гасит «назад», пока выпуск идёт.
  useEffect(() => {
    onBusy?.(phase === 'running')
  }, [phase])

  async function issue() {
    setPhase('running')
    setOutcome('')
    setOfferRevoke(false)
    setSendNotice('')
    const args = issueArgs(pending)
    try {
      const { cmd_id: id, tunnel_name: name } = await issueVPNConfig(routerID, args.provider, args.option, args.instanceID)
      const until = Date.now() + (asleep ? 6 * 60_000 : 90_000)
      while (alive.current && Date.now() < until) {
        const res = await fetchCommandResult(routerID, id, 10)
        if (!alive.current) return
        if (res) {
          setPhase('done')
          setOutcome(
            res.status === 'ok'
              ? `Конфиг выпущен и импортирован как «${name}». Он появится в списке VPN-туннелей.`
              : `Роутер не принял конфиг: ${res.output || res.status}`,
          )
          if (res.status === 'ok') onIssued?.()
          return
        }
      }
      if (!alive.current) return
      setPhase('done')
      setOutcome('Конфиг выпущен, но роутер пока не подтвердил импорт. Откройте экран VPN-туннелей позже.')
    } catch (err) {
      if (!alive.current) return
      const failure = issueFailure(err, perms, pending.provider)
      setPhase('done')
      setOutcome(failure.text)
      setOfferRevoke(failure.offerRevoke)
    }
  }

  function sendConf() {
    const text = sendConfSheetText(pending)
    const args = issueArgs(pending)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        note: text.note,
        buttonLabel: 'Прислать',
        busyLabel: 'Отправляем…',
        errorText: sendConfErrorText,
        perform: () => sendVPNConf(routerID, { provider: args.provider, option: args.option, instanceID: args.instanceID }),
        onDone: () => {
          if (alive.current) setSendNotice(SEND_CONF_DONE)
        },
      }),
    )
  }

  const own = pending.provider === 'selfhosted'

  return (
    <Section title="Что произойдёт">
      <div class="card">
        <DataRow title="Откуда" code={own ? undefined : pending.provider} value={pending.title} />
        <DataRow title="Выпускаем" code={own ? undefined : pending.option.id} value={pending.option.label} />
        <p class="card-foot">
          <Quoted text={issueExplain(pending)} />
        </p>
      </div>
      {phase === 'running' && <p class="state">{CABINET_TEXTS.issueRunning}</p>}
      {phase === 'done' && (
        <p class={`state cabinet-outcome${offerRevoke ? ' state-error' : ''}`}>
          <Quoted text={outcome} />
        </p>
      )}
      {phase === 'done' && offerRevoke && (
        <button type="button" class="btn btn-ghost btn-wide" onClick={onBackToList}>
          {CABINET_TEXTS.backToList}
        </button>
      )}
      {phase !== 'running' && (
        <button type="button" class="btn btn-primary btn-wide" onClick={issue}>
          {phase === 'done' ? 'Выпустить ещё раз' : 'Выпустить и положить на роутер'}
        </button>
      )}
      {perms.sendConf && phase !== 'running' && (
        <button type="button" class="btn btn-ghost btn-wide cabinet-send" onClick={sendConf}>
          Прислать .conf в личку
        </button>
      )}
      {sendNotice && (
        <p class="hint cabinet-notice" role="status">
          {sendNotice}
        </p>
      )}
    </Section>
  )
}
