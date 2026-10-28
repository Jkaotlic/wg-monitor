import { agentReplyText } from '../errorText.js'
import { useState } from 'preact/hooks'
import { placeText } from '../places.js'
import { useCommand } from '../useCommand.js'
import { tunnelRows, rebindTargets, tunnelRuleSummary } from '../routes.js'
import {
  templateGroups,
  templateChoice,
  parseManualTargets,
  addPlanSummary,
  skippedNote,
  manualPlanArgs,
  geoSearch,
  geoTarget,
  geoRuleName,
  geoCountLabel,
} from '../routeAdd.js'
import { confirmSheet } from '../sheet.js'
import { tunnelTargetLabel } from '../labels.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { Chip } from '../ui/Chip.jsx'
import { Q } from '../ui/Q.jsx'

// «Отправить в туннель» -- три шага в одном экране: куда, что и что из этого
// выйдет. Порядок именно такой, потому что первым оператор выбирает VPN-туннель, а
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
  const [geoTags, setGeoTags] = useState(null)
  const [geoQuery, setGeoQuery] = useState('')
  const [geoPicked, setGeoPicked] = useState([])
  const [geoName, setGeoName] = useState('')

  const catalog = useCommand(routerID)
  const geo = useCommand(routerID)
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

  // Гео-теги читаются один раз за экран: ~1800 имён -- десятки килобайт, и
  // искать по ним дальше можно на телефоне, без похода к роутеру на каждую букву.
  function loadGeo() {
    if (geoTags != null || geo.busy) return
    geo.run('route_geo_tags', {}, deadline).then((res) => {
      if (res?.status !== 'ok') return
      try {
        setGeoTags(JSON.parse(res.output)?.tags ?? [])
      } catch {
        setGeoTags([])
      }
    })
  }

  function openGeo() {
    setMode('geo')
    loadGeo()
  }

  function toggleGeo(target) {
    setGeoPicked((prev) => (prev.includes(target) ? prev.filter((t) => t !== target) : [...prev, target]))
  }

  function askGeoPlan() {
    const ruleName = geoName.trim() || geoRuleName(geoPicked)
    askPlan(
      { kind: 'dns', tunnel_id: tunnel.id, targets: geoPicked, name: ruleName, use_hr_neo: true },
      { title: ruleName, sub: 'по гео-тегам HydraRoute Neo' },
    )
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
        setSummary({ ...addPlanSummary(JSON.parse(res.output), { hrNeoRunning }), args })
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
    const args = manualPlanArgs({ parsed: manualParsed, tunnelID: tunnel.id, name, hrNeoRunning })
    askPlan(args, { title: args.name, sub: manualParsed.kind === 'dns' ? 'по имени сайта' : 'по адресу сети' })
  }

  function apply() {
    openSheet(
      confirmSheet({
        routerID,
        title: `Отправить «${summary.title}» в «${tunnel.name}»?`,
        body: [
          summary.lines.length
            ? `В обход пойдёт: ${summary.lines.join(' · ')}`
            : `Правило появится на роутере и будет вести в «${tunnel.name}».`,
          summary.engineWarning || summary.engine,
        ].join('. '),
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
    <Overlay title="Добавить сайт или адрес" backLabel={step === 'where' ? 'Маршруты' : 'Назад'} onBack={back}>
      <div class="screen">
        {step === 'where' && (
          <Section title="Куда вести трафик">
            {tunnels.length ? (
              <ul class="card list-reset">
                {tunnels.map((t) => (
                  <ListRow
                    key={t.id}
                    title={t.name}
                    sub={`${tunnelTargetLabel(t)} · ${tunnelRuleSummary(t)}`}
                    onClick={() => pickTunnel(t)}
                  />
                ))}
              </ul>
            ) : (
              <div class="card">
                <p class="traffic-detail">Роутер не сообщил ни одного своего VPN-туннеля — отправлять некуда.</p>
              </div>
            )}
          </Section>
        )}

        {step === 'what' && (
          <>
            <p class="router-lastseen">
              Пойдёт в <Q>{tunnel.name}</Q>.
            </p>
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
              {/* Гео-теги разворачивает только HydraRoute Neo: без него
                  вкладка обещала бы правило, которое ничего не ведёт. */}
              {hrNeoRunning && (
                <button type="button" class={`seg-btn${mode === 'geo' ? ' seg-btn-on' : ''}`} onClick={openGeo}>
                  Гео
                </button>
              )}
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

            {mode === 'geo' && hrNeoRunning && (
              <Section title="Гео-теги роутера">
                <div class="card card-form">
                  <div class="field">
                    <label for="route-add-geo">Сервис или страна</label>
                    <input
                      id="route-add-geo"
                      type="search"
                      value={geoQuery}
                      placeholder="youtube, telegram, ru"
                      onInput={(e) => setGeoQuery(e.currentTarget.value)}
                    />
                  </div>
                  {geo.busy && <p class="state">Читаем гео-теги роутера…</p>}
                  {geo.error && <p class="state state-error">{geo.error}</p>}
                  {geo.result && geo.result.status !== 'ok' && (
                    <p class="state state-error">{agentReplyText(geo.result, 'Роутер не отдал гео-теги — попробуйте ещё раз через минуту.')}</p>
                  )}
                  {geoTags != null && geoTags.length === 0 && !geo.busy && (
                    <p class="state">На роутере нет гео-файлов HydraRoute Neo.</p>
                  )}
                  {geoPicked.length > 0 && (
                    <p class="rules-target">{geoPicked.join(', ')}</p>
                  )}
                </div>
                {geoTags != null && geoQuery.trim() !== '' && (() => {
                  const found = geoSearch(geoTags, geoQuery)
                  return found.length ? (
                    <ul class="card list-reset">
                      {found.map((tag) => {
                        const target = geoTarget(tag)
                        const on = geoPicked.includes(target)
                        return (
                          <ListRow
                            key={target}
                            title={target}
                            sub={geoCountLabel(tag)}
                            right={on ? <Chip tone="ok">выбран</Chip> : undefined}
                            onClick={() => toggleGeo(target)}
                          />
                        )
                      })}
                    </ul>
                  ) : (
                    <p class="state">Такого тега на роутере нет.</p>
                  )
                })()}
                {geoPicked.length > 0 && (
                  <div class="card card-form">
                    <div class="field">
                      <label for="route-add-geo-name">Название правила</label>
                      <input
                        id="route-add-geo-name"
                        type="text"
                        value={geoName}
                        placeholder={geoRuleName(geoPicked)}
                        onInput={(e) => setGeoName(e.currentTarget.value)}
                      />
                    </div>
                    <button type="button" class="btn btn-primary" onClick={askGeoPlan}>
                      Показать, что изменится
                    </button>
                  </div>
                )}
              </Section>
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
                <p class="state state-error">{agentReplyText(plan.result, 'Роутер не смог собрать превью — попробуйте ещё раз через минуту.')}</p>
              )}
              {summary && (
                <>
                  <p class="traffic-detail">
                    Пойдёт в <Q>{tunnel.name}</Q>:
                  </p>
                  {summary.targets && <p class="rules-target">{summary.targets}</p>}
                  <p class="traffic-detail">{summary.engine}</p>
                  {summary.engineWarning && <p class="traffic-note">{summary.engineWarning}</p>}
                  {summary.notes.map((note) => (
                    <p key={note} class="traffic-note">{note}</p>
                  ))}
                  {!summary.canApply && (
                    <p class="state state-error">
                      {`Применить нельзя: правило спорит с уже существующим. Уберите прежнее в разделе ${placeText('routes')} или выберите другой набор.`}
                    </p>
                  )}
                </>
              )}
            </div>
            {summary?.canApply && (
              <button type="button" class="btn btn-primary btn-wide" onClick={apply}>
                Отправить в обход
              </button>
            )}
          </Section>
        )}
      </div>
    </Overlay>
  )
}
