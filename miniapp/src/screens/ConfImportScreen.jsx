import { useEffect, useRef, useState } from 'preact/hooks'
import { previewTunnelImport, fetchTunnelImport, confirmTunnelImport } from '../api.js'
import { waitCommand, waitDeadlineMs, repeatWhilePending } from '../commandWait.js'
import {
  confFileProblem,
  readConfBase64,
  suggestTunnelName,
  tunnelNameProblem,
  previewView,
  importErrorText,
  importOutcome,
  withPickAgain,
  IMPORT_POLL_DEADLINE_MS,
  IMPORT_TEXTS,
} from '../confImport.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { TextField } from '../ui/FormField.jsx'
import { Quoted } from '../ui/Q.jsx'

// Коды, после которых этот конфиг уже не добавить: файл выбирается заново.
const RESTART_CODES = new Set(['preview_expired', 'conf_rejected'])
// Коды «роутер ещё проверяет»: не ошибка, а повод опрашивать дальше.
const PENDING_CODES = new Set(['preview_not_ready', 'analysis_pending'])

// «Загрузить конфиг .conf»: свой конфиг встаёт на роутер НОВЫМ VPN-туннелем.
// Локальный слой вкладки, в адрес не пишется.
//
// В файле приватный ключ. Содержимое живёт только в confRef -- не в состоянии
// (его видят инструменты разработчика и оно переживает рендеры), не в
// навигации, не в DOM -- и стирается в момент отправки на проверку. Дальше
// сервер держит конфиг под токеном предпросмотра; опрос и «Добавить» шлют
// только токен.
export function ConfImportScreen({ routerID, asleep, snapshot, onClose, onImported }) {
  const confRef = useRef('')
  const [hasConf, setHasConf] = useState(false)
  const [fileName, setFileName] = useState('')
  const [fileProblem, setFileProblem] = useState('')
  const [name, setName] = useState('')
  // pick -- выбор файла и имени; checking -- ждём предпросмотр (и опрашиваем,
  // пока роутер проверяет); preview -- показываем; adding -- ждём роутер;
  // done -- итог.
  const [phase, setPhase] = useState('pick')
  const [preview, setPreview] = useState(null)
  const [error, setError] = useState('')
  const [outcome, setOutcome] = useState(null)
  const alive = useRef(true)
  // Номер выбора файла: чтение прежнего файла, закончившееся после нового
  // выбора, отбрасывается.
  const pickSeq = useRef(0)
  // Защита от двойного касания «Добавить»: состояние обновится только после
  // рендера, а второе касание успевает раньше.
  const adding = useRef(false)
  useEffect(
    () => () => {
      alive.current = false
      confRef.current = ''
    },
    [],
  )

  const nameProblem = tunnelNameProblem(name, snapshot)
  const busy = phase === 'checking' || phase === 'adding'
  const view = preview?.view ?? null

  function forget() {
    confRef.current = ''
    setHasConf(false)
  }

  function reset() {
    forget()
    setPreview(null)
    setOutcome(null)
    setError('')
    setFileName('')
    setFileProblem('')
    setPhase('pick')
  }

  async function pick(e) {
    const input = e.currentTarget
    const file = input.files?.[0] ?? null
    // Поле очищается сразу: выбранный файл не висит в форме, а повторный выбор
    // того же файла снова вызовет onChange.
    input.value = ''
    const seq = ++pickSeq.current
    forget()
    setPreview(null)
    setOutcome(null)
    setError('')
    setPhase('pick')
    setFileName(file?.name ?? '')
    const problem = confFileProblem(file)
    setFileProblem(problem)
    if (problem) return
    if (!name.trim()) setName(suggestTunnelName(file.name))
    try {
      const b64 = await readConfBase64(file)
      if (!alive.current || seq !== pickSeq.current) return
      confRef.current = b64
      setHasConf(true)
    } catch {
      if (alive.current && seq === pickSeq.current) setFileProblem(IMPORT_TEXTS.readFailed)
    }
  }

  function failed(err) {
    if (RESTART_CODES.has(err?.code)) {
      setError(withPickAgain(importErrorText(err)))
      setFileName('')
      setPreview(null)
      setPhase('pick')
      return true
    }
    setError(importErrorText(err))
    return false
  }

  // Роутер ещё проверяет конфиг (state:"analyzing") -- спрашивать сервер по
  // токену, пока не ответит. Не дождались -- показать то, что есть, со
  // словами и кнопкой «Проверить ещё раз».
  async function settle(resp, wanted) {
    let last = resp
    if (resp?.state === 'analyzing' && resp.token) {
      setPreview({ view: previewView(resp), token: resp.token, name: resp.name || wanted })
      // 409 analysis_pending на опросе -- то же «ещё проверяет».
      const once = () =>
        fetchTunnelImport(routerID, resp.token).catch((err) => {
          if (err?.code === 'analysis_pending') return { ...resp, state: 'analyzing' }
          throw err
        })
      const { resp: polled } = await repeatWhilePending(once, {
        pending: (r) => r?.state === 'analyzing',
        // Токен живёт 5 минут -- опрос кончается раньше, и спящему роутеру тоже.
        deadlineMs: IMPORT_POLL_DEADLINE_MS,
        alive: () => alive.current,
      })
      if (!alive.current) return
      last = polled ?? resp
    }
    setPreview({ view: previewView(last), token: last?.token || resp?.token || '', name: last?.name || wanted })
    setPhase('preview')
  }

  async function check() {
    const conf = confRef.current
    const wanted = name.trim()
    if (!conf || nameProblem || busy) return
    // Стирается до ответа: и при успехе, и при отказе второй раз этот конфиг
    // с клиента не уйдёт.
    forget()
    setPhase('checking')
    setError('')
    try {
      const resp = await previewTunnelImport(routerID, { name: wanted, confB64: conf })
      if (!alive.current) return
      await settle(resp, wanted)
    } catch (err) {
      if (!alive.current) return
      // Проверка ещё идёт под уже выданным токеном -- опрашивать его.
      if (err?.code === 'analysis_pending' && err.data?.token) {
        try {
          await settle({ token: err.data.token, name: wanted, state: 'analyzing' }, wanted)
          return
        } catch (again) {
          if (!alive.current) return
          err = again
        }
      }
      // Конфиг уже стёрт с клиента: файл выбирается заново, и прежнее имя
      // файла не должно обещать, что он ещё здесь.
      setError(withPickAgain(importErrorText(err)))
      setFileName('')
      setPreview(null)
      setPhase('pick')
    }
  }

  function recheck() {
    if (!preview?.token || busy) return
    setError('')
    recheckToken(preview.token, preview.name)
  }

  async function recheckToken(token, wanted) {
    setPhase('checking')
    try {
      await settle({ token, name: wanted, state: 'analyzing' }, wanted)
    } catch (err) {
      if (!alive.current) return
      if (!failed(err)) setPhase('preview')
    }
  }

  async function add() {
    if (adding.current || !preview?.token || !preview.view.canConfirm || busy) return
    adding.current = true
    const { token, name: wanted } = preview
    setPreview((p) => (p ? { ...p, token: '' } : p))
    setPhase('adding')
    setError('')
    let sent
    try {
      sent = await confirmTunnelImport(routerID, token)
    } catch (err) {
      adding.current = false
      if (!alive.current) return
      if (PENDING_CODES.has(err?.code)) {
        // Роутер ещё проверяет: не ошибка -- показать «Проверка ещё идёт» и
        // опрашивать дальше.
        setPreview((p) => (p ? { ...p, token, view: { ...p.view, analyzing: true, canConfirm: false } } : p))
        await recheckToken(token, wanted)
        return
      }
      if (failed(err)) return
      setPreview((p) => (p ? { ...p, token } : p))
      setPhase('preview')
      return
    }
    // Команда уже в очереди: сорвавшееся ожидание -- не «попробуйте ещё», а
    // «роутер пока не ответил».
    let result = null
    try {
      result = await waitCommand(routerID, sent.cmd_id, {
        deadlineMs: waitDeadlineMs(asleep || sent?.router_asleep === true),
        alive: () => alive.current,
      })
    } catch {
      result = null
    }
    adding.current = false
    if (!alive.current) return
    setOutcome(importOutcome(result, sent?.tunnel_name || wanted))
    setPhase('done')
    onImported?.()
  }

  const stillAnalyzing = Boolean(view?.analyzing)

  return (
    <Overlay title={IMPORT_TEXTS.title} backLabel="VPN-туннели" onBack={onClose}>
      <div class="screen conf-import">
        <h1 class="screen-title">{IMPORT_TEXTS.title}</h1>
        <p class="router-lastseen">{IMPORT_TEXTS.intro}</p>

        {phase !== 'done' && !preview && (
          <Section title="Файл">
            {/* Поле лежит прозрачным слоем поверх кнопки: касание попадает в
                само поле, программный click() некоторые webview блокируют.
                Фильтра accept нет: Telegram на iOS и Android с ним не даёт
                выбрать .conf; расширение и размер проверяет confFileProblem. */}
            <label class={`btn btn-ghost btn-wide conf-pick${busy ? ' conf-pick-off' : ''}`}>
              {fileName ? IMPORT_TEXTS.pickAnother : IMPORT_TEXTS.pick}
              <input type="file" class="conf-pick-input" disabled={busy} onChange={pick} />
            </label>
            {fileName && !fileProblem && <p class="hint conf-file-name">{`Файл: ${fileName}`}</p>}
            {fileProblem && (
              <p class="state state-error" role="alert">
                {fileProblem}
              </p>
            )}
            <p class="hint">{IMPORT_TEXTS.privacy}</p>
            <TextField
              id="conf-import-name"
              label="Имя VPN-туннеля"
              value={name}
              onInput={setName}
              placeholder="например, amsterdam"
              hint={IMPORT_TEXTS.nameHint}
              error={name ? nameProblem : ''}
            />
            <button type="button" class="btn btn-primary btn-wide" disabled={!hasConf || Boolean(nameProblem) || busy} onClick={check}>
              {phase === 'checking' ? 'Проверяем…' : 'Проверить конфиг'}
            </button>
          </Section>
        )}

        {view && phase !== 'done' && (
          <Section title="Что в конфиге">
            <div class="card">
              <DataRow title="Сервер" value={view.endpoint} />
              <DataRow title="Адреса" value={view.addresses} />
              <DataRow title="DNS" value={view.dns} />
              <DataRow title="MTU" value={view.mtu} />
              {stillAnalyzing && <p class="card-foot">{phase === 'checking' ? IMPORT_TEXTS.analyzing : IMPORT_TEXTS.analyzeSlow}</p>}
              {!stillAnalyzing && !view.analyzed && (
                <p class="card-foot">
                  <Quoted text={view.note || IMPORT_TEXTS.notAnalyzed} />
                </p>
              )}
              {!stillAnalyzing && view.analyzed && view.problems.length === 0 && <p class="card-foot">{IMPORT_TEXTS.analyzedClean}</p>}
            </div>
            {view.problems.length > 0 && (
              <ul class="card list-reset conf-problems">
                {view.problems.map((p, i) => (
                  <li key={i} class={`conf-problem conf-problem-${p.tone}`}>
                    <Quoted text={p.text} />
                  </li>
                ))}
              </ul>
            )}
            {view.blocking && (
              <p class="state state-error" role="alert">
                {IMPORT_TEXTS.blocking}
              </p>
            )}
          </Section>
        )}

        {view && phase !== 'done' && (
          <Section title="Что произойдёт">
            <div class="card">
              <DataRow title="Новый VPN-туннель" value={`«${preview.name}»`} />
              <p class="card-foot">
                <Quoted text={IMPORT_TEXTS.replaceHint} />
              </p>
            </div>
            <button type="button" class="btn btn-primary btn-wide" disabled={!preview.token || !view.canConfirm || busy} onClick={add}>
              {phase === 'adding' ? 'Добавляем…' : 'Добавить как новый'}
            </button>
            {stillAnalyzing && phase !== 'checking' && (
              <button type="button" class="btn btn-ghost btn-wide" disabled={busy} onClick={recheck}>
                Проверить ещё раз
              </button>
            )}
            <button type="button" class="btn btn-ghost btn-wide" disabled={busy} onClick={reset}>
              Выбрать другой файл
            </button>
          </Section>
        )}

        {phase === 'adding' && <p class="state">{IMPORT_TEXTS.running}</p>}
        {error && (
          <p class="state state-error" role="alert">
            <Quoted text={error} />
          </p>
        )}

        {outcome && (
          <>
            <p class={`state tunnel-outcome${outcome.tone === 'error' ? ' state-error' : ''}`} role="status">
              <Quoted text={outcome.text} />
            </p>
            <button type="button" class="btn btn-primary btn-wide" onClick={onClose}>
              К списку VPN-туннелей
            </button>
            {!outcome.done && (
              <button type="button" class="btn btn-ghost btn-wide" onClick={reset}>
                Выбрать другой файл
              </button>
            )}
          </>
        )}
      </div>
    </Overlay>
  )
}
