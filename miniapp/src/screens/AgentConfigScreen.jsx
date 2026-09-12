import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchRouterSettings } from '../api.js'
import { useCommand } from '../useCommand.js'
import { confirmSheet } from '../sheet.js'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import {
  AGENT_CONFIG_TEXTS,
  agentConfigArgs,
  agentConfigAvailable,
  agentConfigConfirmBody,
  agentConfigFields,
  agentConfigRows,
  parseAgentConfig,
  validateAgentConfig,
} from '../agentConfig.js'

// «Правка конфига агента» -- экран, перенесённый из операторского дашборда.
//
// Две вещи в нём важнее вёрстки, и обе про то, чего экран НЕ делает:
//
//   - У роутера с агентом ниже пола версии полей нет вовсе, а не «есть, но
//     серые». Серая кнопка обещает, что настройка существует и когда-нибудь
//     нажмётся; строка «появится после обновления агента» говорит правду.
//     Это вторая из двух независимых преград: первая -- отказ бэкенда
//     ставить такую команду в очередь.
//   - Секреты не показываются: путь своего DNS-сервера приезжает
//     замаскированным и сворачивается в «задано», пароля панели здесь нет
//     вовсе -- бэкенд его не знает и не хранит.
export function AgentConfigScreen({ routerID, routerName, asleep, openSheet, onClose }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [settings, setSettings] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [values, setValues] = useState({})
  const [formError, setFormError] = useState('')
  const read = useCommand(routerID)

  useEffect(() => {
    fetchRouterSettings(routerID)
      .then((s) => {
        setSettings(s)
        setLoadError(null)
      })
      .catch(() => setLoadError('Не удалось прочитать настройки роутера.'))
  }, [routerID])

  const available = agentConfigAvailable(settings)

  // Конфиг спрашиваем только у роутера, которому эту правку вообще можно
  // предлагать: лишний вопрос старому агенту рисовал бы форму, которой всё
  // равно неоткуда взяться.
  const askedRef = useRef(false)
  useEffect(() => {
    if (!available || askedRef.current) return
    askedRef.current = true
    read.run('agent_config_get', {}, deadline)
  }, [available])

  const view = read.result?.status === 'ok' ? parseAgentConfig(read.result.output) : null

  function fieldValue(field) {
    if (field.key in values) return values[field.key]
    if (!view) return field.kind === 'bool' ? false : ''
    return field.kind === 'bool' ? Boolean(view[field.key]) : (view[field.key] ?? '')
  }

  function askSave() {
    const edited = {}
    for (const f of agentConfigFields()) {
      if (f.key in values) edited[f.key] = f.kind === 'bool' ? Boolean(values[f.key]) : values[f.key]
    }
    const { error } = validateAgentConfig(edited)
    if (error) {
      setFormError(error)
      return
    }
    const args = agentConfigArgs(view ?? {}, edited)
    if (Object.keys(args).length === 0) {
      setFormError(AGENT_CONFIG_TEXTS.nothingChanged)
      return
    }
    setFormError('')
    openSheet(
      confirmSheet({
        routerID,
        title: `Изменить настройки агента на «${routerName}»?`,
        body: agentConfigConfirmBody(routerName),
        action: 'update_agent_config',
        args,
        buttonLabel: 'Изменить и перезапустить',
        danger: true,
        asleep,
        // Набор имени роутера -- пауза, а не защита от чужого пальца: правка
        // перезапускает агента, и роутер замолчит на несколько секунд.
        confirmPhrase: routerName || '',
        onDone: () => {
          setValues({})
          askedRef.current = true
          read.run('agent_config_get', {}, deadline)
        },
      }),
    )
  }

  return (
    <Overlay title="Настройки агента" backLabel="Обслуживание" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">{routerName || 'Роутер'}</h1>

        {loadError && <p class="state state-error">{loadError}</p>}

        {/* Не админу экран не рисуется: сервер ему всё равно откажет, а
            серые поля не объясняют, почему нельзя. */}
        {settings && settings.role !== 'admin' && <p class="hint">{AGENT_CONFIG_TEXTS.adminOnly}</p>}

        {/* Агент ниже пола версии: полей нет вовсе. */}
        {settings && settings.role === 'admin' && !available && (
          <p class="hint">
            {AGENT_CONFIG_TEXTS.tooOld} Агент на роутере: {settings.agent_version || 'версию не сообщал'}.
          </p>
        )}

        {available && (
          <>
            <Section title="Что сейчас на роутере">
              {read.error && <p class="state state-error">{read.error}</p>}
              {read.result && read.result.status !== 'ok' && (
                <p class="state state-error">Роутер не ответил: {read.result.output || read.result.status}</p>
              )}
              {!view ? (
                <p class="state">Спрашиваем роутер…</p>
              ) : (
                <div class="card settings-card">
                  {agentConfigRows(view).map((r) => (
                    <DataRow key={r.key} title={r.title} value={r.value} />
                  ))}
                  <p class="card-foot">{AGENT_CONFIG_TEXTS.watchdogHidden}</p>
                  <p class="card-foot">{AGENT_CONFIG_TEXTS.panelPassword}</p>
                </div>
              )}
            </Section>

            {view && (
              <Section title="Что меняем">
                <div class="card settings-card">
                  {agentConfigFields().map((f) => (
                    <div key={f.key} class="field">
                      <label for={`agent-cfg-${f.key}`}>
                        {f.title}
                        {f.unit ? `, ${f.unit}` : ''}
                      </label>
                      {f.kind === 'bool' ? (
                        <input
                          id={`agent-cfg-${f.key}`}
                          type="checkbox"
                          checked={Boolean(fieldValue(f))}
                          onInput={(e) => setValues({ ...values, [f.key]: e.currentTarget.checked })}
                        />
                      ) : (
                        <input
                          id={`agent-cfg-${f.key}`}
                          type={f.kind === 'int' ? 'number' : 'text'}
                          autocomplete="off"
                          value={String(fieldValue(f))}
                          onInput={(e) => setValues({ ...values, [f.key]: e.currentTarget.value })}
                        />
                      )}
                    </div>
                  ))}
                  <p class="card-foot">{AGENT_CONFIG_TEXTS.watchdogElsewhere}</p>
                  <p class="card-foot">{AGENT_CONFIG_TEXTS.restart}</p>
                </div>
                {formError && <p class="state state-error">{formError}</p>}
                <button type="button" class="btn btn-danger btn-wide" onClick={askSave}>
                  Изменить настройки агента
                </button>
              </Section>
            )}
          </>
        )}
      </div>
    </Overlay>
  )
}
