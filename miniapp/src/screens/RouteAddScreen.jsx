import { useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { tunnelRows, rebindTargets, tunnelRuleSummary } from '../routes.js'
import { templateGroups, templateChoice, parseManualTargets, addPlanSummary, skippedNote } from '../routeAdd.js'
import { confirmSheet } from '../sheet.js'
import { tunnelLiveLabel } from '../labels.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { Chip } from '../ui/Chip.jsx'

// «Отправить в туннель» -- три шага в одном экране: куда, что и что из этого
// выйдет. Порядок именно такой, потому что первым оператор выбирает линию, а
// не механизм: вопрос "DNS или static" на этом экране не задаётся вовсе, он
// читается из того, что человек написал (parseManualTargets).
//
// Экран открывается поверх «Маршрутов» своим слоем и держит собственную
// кнопку «назад». Системная кнопка Telegram закрывает весь слой маршрутов --
// она принадлежит навигации приложения, а не этому экрану; терять при этом
// нечего, ничего ещё не применено.
export function RouteAddScreen({ routerID, asleep, snapshot, openSheet, onClose, onApplied }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [step, setStep] = useState('where')
  const [tunnel, setTunnel] = useState(null)
  const [mode, setMode] = useState('catalog')
  const [manual, setManual] = useState('')
  const [name, setName] = useState('')
  const [templates, setTemplates] = useState(null)
  const [skipped, setSkipped] = useState(0)
  const [choice, setChoice] = useState(null)
  const [summary, setSummary] = useState(null)

  const catalog = useCommand(routerID)
  const plan = useCommand(routerID)

  const hrNeoRunning = Boolean(snapshot?.hr_neo?.installed && snapshot?.hr_neo?.running)
  const tunnels = rebindTargets(tunnelRows(snapshot))
  const manualParsed = parseManualTargets(manual)

  function loadCatalog() {
    if (templates != null || catalog.busy) return
    catalog.run('route_templates', {}, deadline).then((res) => {
      if (res?.status !== 'ok') return
      try {
        const payload = JSON.parse(res.output)
        setTemplates(payload?.templates ?? [])
        setSkipped(payload?.skipped ?? 0)
      } catch {
        setTemplates([])
        setSkipped(0)
      }
    })
  }

  function pickTunnel(row) {
    setTunnel(row)
    setStep('what')
    loadCatalog()
  }

  // Превью считает агент: он один знает живые правила роутера и видит
  // пересечения. Экран не угадывает их сам -- угаданное пересечение было бы
  // догадкой, поданной как факт.
  function askPlan(args, picked) {
    setChoice(picked)
    setSummary(null)
    setStep('preview')
    plan.run('route_add_plan', args, deadline).then((res) => {
      if (res?.status !== 'ok') return
      try {
        setSummary({ ...addPlanSummary(JSON.parse(res.output)), args })
      } catch {
        setSummary(null)
      }
    })
  }

  function pickTemplate(tpl) {
    const c = templateChoice(tpl, { hrNeoRunning })
    if (!c.canApply) return
    askPlan({ ...c.args, tunnel_id: tunnel.id }, { title: c.name, sub: c.summary })
  }

  function askManualPlan() {
    const ruleName = name.trim() || manualParsed.targets[0]
    askPlan(
      {
        kind: manualParsed.kind,
        tunnel_id: tunnel.id,
        targets: manualParsed.targets,
        name: ruleName,
      },
      { title: ruleName, sub: manualParsed.kind === 'dns' ? 'по имени сайта' : 'по адресу сети' },
    )
  }

  function apply() {
    openSheet(
      confirmSheet({
        routerID,
        title: `Отправить «${summary.title}» в «${tunnel.name}»?`,
        body: summary.lines.length
          ? `В туннель пойдёт: ${summary.lines.join(' · ')}`
          : `Правило появится на роутере и будет вести в «${tunnel.name}».`,
        action: 'route_add',
        args: { ...summary.args, draft_hash: summary.hash },
        buttonLabel: 'Отправить',
        asleep,
        onDone: () => {
          onApplied?.()
          onClose()
        },
      }),
    )
  }

  const back = () => {
    if (step === 'preview') return setStep('what')
    if (step === 'what') return setStep('where')
    return onClose()
  }

  return (
    <Overlay title="Отправить в туннель" backLabel={step === 'where' ? 'Маршруты' : 'Назад'} onBack={back}>
      <div class="screen">
        {step === 'where' && (
          <Section title="Куда вести трафик">
            {tunnels.length ? (
              <ul class="card list-reset">
                {tunnels.map((t) => (
                  <ListRow
                    key={t.id}
                    title={t.name}
                    sub={`${tunnelLiveLabel(t.live)} · ${tunnelRuleSummary(t)}`}
                    onClick={() => pickTunnel(t)}
                  />
                ))}
              </ul>
            ) : (
              <div class="card">
                <p class="traffic-detail">Роутер не сообщил ни одного своего туннеля — отправлять некуда.</p>
              </div>
            )}
          </Section>
        )}

        {step === 'what' && (
          <>
            <p class="router-lastseen">Пойдёт в «{tunnel.name}».</p>
            <div class="seg">
              <button
                type="button"
                class={`seg-btn${mode === 'catalog' ? ' seg-btn-on' : ''}`}
                onClick={() => setMode('catalog')}
              >
                Из каталога
              </button>
              <button
                type="button"
                class={`seg-btn${mode === 'manual' ? ' seg-btn-on' : ''}`}
                onClick={() => setMode('manual')}
              >
                Вручную
              </button>
            </div>

            {mode === 'catalog' && (
              <>
                {catalog.busy && <p class="state">Читаем каталог роутера…</p>}
                {catalog.error && <p class="state state-error">{catalog.error}</p>}
                {templates != null && templates.length === 0 && !catalog.busy && (
                  <p class="state">Каталог роутера пуст — заведите правило вручную.</p>
                )}
                {skippedNote(skipped) && <p class="hint">{skippedNote(skipped)}</p>}
                {templateGroups(templates ?? []).map((g) => (
                  <Section key={g.category} title={g.category}>
                    <ul class="card list-reset">
                      {g.items.map((tpl) => {
                        const c = templateChoice(tpl, { hrNeoRunning })
                        return (
                          <ListRow
                            key={tpl.id}
                            title={c.name}
                            sub={c.canApply ? c.summary : c.reason}
                            tone={c.canApply ? undefined : 'muted'}
                            onClick={c.canApply ? () => pickTemplate(tpl) : undefined}
                          />
                        )
                      })}
                    </ul>
                  </Section>
                ))}
              </>
            )}

            {mode === 'manual' && (
              <Section title="Что отправить">
                <div class="card card-form">
                  <div class="field">
                    <label for="route-add-targets">Сайты или сети</label>
                    <textarea
                      id="route-add-targets"
                      rows="3"
                      value={manual}
                      placeholder="openai.com, chatgpt.com"
                      onInput={(e) => setManual(e.currentTarget.value)}
                    />
                  </div>
                  {/* Тип правила не спрашиваем: он и так виден из введённого,
                      а вопрос про DNS или static -- вопрос про механизм. */}
                  {manualParsed.kind && (
                    <p class="traffic-detail">
                      {manualParsed.kind === 'dns'
                        ? `${manualParsed.targets.length} шт. — по имени сайта`
                        : `${manualParsed.targets.length} шт. — по адресу сети`}
                    </p>
                  )}
                  {manualParsed.error && <p class="state state-error">{manualParsed.error}</p>}
                  <div class="field">
                    <label for="route-add-name">Название правила</label>
                    <input
                      id="route-add-name"
                      type="text"
                      value={name}
                      placeholder={manualParsed.targets[0] ?? 'например, Работа'}
                      onInput={(e) => setName(e.currentTarget.value)}
                    />
                  </div>
                  <button
                    type="button"
                    class="btn btn-primary"
                    disabled={!manualParsed.kind || manualParsed.targets.length === 0}
                    onClick={askManualPlan}
                  >
                    Показать, что изменится
                  </button>
                </div>
              </Section>
            )}
          </>
        )}

        {step === 'preview' && (
          <Section title="Что изменится">
            <div class="card">
              <p class="traffic-title">{summary?.title ?? choice?.title}</p>
              {plan.busy && <p class="state">Роутер считает, что получится…</p>}
              {plan.error && <p class="state state-error">{plan.error}</p>}
              {plan.result && plan.result.status !== 'ok' && (
                <p class="state state-error">{plan.result.output || 'Роутер не смог собрать превью'}</p>
              )}
              {summary && (
                <>
                  <p class="traffic-detail">Пойдёт в «{tunnel.name}»:</p>
                  {summary.targets && <p class="rules-target">{summary.targets}</p>}
                  {summary.notes.map((note) => (
                    <p key={note} class="traffic-note">{note}</p>
                  ))}
                  {!summary.canApply && (
                    <p class="state state-error">
                      Применить нельзя: правило спорит с уже существующим. Уберите прежнее на
                      экране «Маршруты» или выберите другой набор.
                    </p>
                  )}
                </>
              )}
            </div>
            {summary?.canApply && (
              <button type="button" class="btn btn-primary btn-wide" onClick={apply}>
                Отправить в туннель
              </button>
            )}
          </Section>
        )}
      </div>
    </Overlay>
  )
}
