import { useEffect, useState } from 'preact/hooks'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { AccessSection } from './AccessSection.jsx'
import { ParkSection } from './ParkSection.jsx'
import { fetchAgentConnection, repointRouterAgent } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  JOB_SECRET_NOTE,
  PANEL_ADDRESS_MISSING,
  repointSheetText,
  repointFields,
  repointReady,
  repointRequestBody,
  repointJobTitle,
  jobStartErrorText,
} from '../agentJobs.js'

// Администрирование: парк целиком и доступы к этому роутеру.
//
// «Парк» -- своя секция (ParkSection.jsx): там обновление агентов, раскатка
// бэкенда, добавление роутера и массовые действия. openLayer открывает слои
// парка (мастер, ход работы, ожидание раскатки) с возвратом сюда. Здесь --
// только входы в экраны радиуса одного роутера.
//
// «Опасное» свёрнуто (спека, п. 8): перенаправление уводит роутер с этого
// сервера, и случайно раскрыть его пролистыванием нельзя. Запуск ведёт на
// «Ход работы» через openLayer (возврат -- сюда же).
export function AdminOverlay({ routerID, routerName = '', isAdmin = false, onClose, openSheet, openLayer, onOpenAgentConfig, onOpenAgentConnection, onOpenDNSReset, onOpenPackages, onOpenRouter, onOpenRouterConnection }) {
  const router = { id: routerID, nickname: routerName }

  // Перенаправление идёт через терминал панели awg-manager: без записанного
  // адреса панели сервер откажет no_awgm_url. Адрес знает только
  // «Подключение агента» (админский маршрут) -- спрашиваем его. null --
  // неизвестно (не ответил): тогда кнопка есть, и решит сервер.
  const [panelKnown, setPanelKnown] = useState(null)
  useEffect(() => {
    if (!isAdmin || !routerName) return undefined
    let alive = true
    setPanelKnown(null)
    fetchAgentConnection(routerID)
      .then((conn) => {
        if (alive) setPanelKnown(Boolean(String(conn?.awgm_url ?? '').trim()))
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [routerID, isAdmin, routerName])

  function askRepoint() {
    const text = repointSheetText(router)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Перенаправить',
        busyLabel: 'Запускаем…',
        danger: true,
        confirmPhrase: routerName,
        fields: repointFields(),
        fieldsReady: repointReady,
        note: JOB_SECRET_NOTE,
        errorText: jobStartErrorText,
        perform: (typed, values) => repointRouterAgent(routerID, repointRequestBody(values, typed)),
        onDone: (resp) => {
          if (resp?.job_id && openLayer) openLayer('job', { jobId: resp.job_id, title: repointJobTitle(router) })
        },
      }),
    )
  }

  return (
    <Overlay title="Обслуживание и доступы" backLabel="Роутер" onBack={onClose}>
      <div class="screen">
        {isAdmin && (
          <ParkSection
            openSheet={openSheet}
            onOpenRouter={onOpenRouter}
            currentID={routerID}
            openLayer={openLayer}
            onOpenConnection={onOpenRouterConnection}
          />
        )}

        {/* Настройки агента -- вход только у админа: радиус правки
            router-global, и сервер ответит остальным 404. Сам экран
            проверяет ещё и версию агента: у старого поля не рисуются. */}
        {isAdmin && onOpenAgentConfig && (
          <Section title="Настройки агента">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenAgentConfig}>
              Открыть настройки агента
            </button>
            <p class="hint">
              Как часто роутер отчитывается, адрес и логин его панели, что агенту разрешено делать
              с устройством. Изменение перезапускает агента.
            </p>
          </Section>
        )}

        {/* Подключение агента -- то, что хранит сервер: как он добирается до
            роутера. Только админ; правка агента не перезапускает. */}
        {isAdmin && onOpenAgentConnection && (
          <Section title="Подключение агента">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenAgentConnection}>
              Открыть подключение агента
            </button>
            <p class="hint">
              Адрес панели awg-manager, SSH, способ раскатки и MAC роутера — как сервер добирается
              до этого роутера.
            </p>
          </Section>
        )}

        {/* Сброс DNS -- вход только у админа (радиус router-global, сервер
            ответит остальным 404). Экран сам проверяет версию агента и
            начинает с предпросмотра. */}
        {isAdmin && onOpenDNSReset && (
          <Section title="Сброс DNS">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenDNSReset}>
              Открыть сброс DNS
            </button>
            <p class="hint">
              Заменить DNS-серверы роутера эталонными. Сначала экран покажет, что изменится.
            </p>
          </Section>
        )}

        {isAdmin && onOpenPackages && (
          <Section title="Пакеты по расписанию">
            <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenPackages}>
              Открыть пакеты по расписанию
            </button>
            <p class="hint">
              Обновление пакетов Entware и очистка Entware по расписанию на самом роутере.
            </p>
          </Section>
        )}

        <AccessSection routerID={routerID} openSheet={openSheet} />

        {isAdmin && routerName && (
          <details class="danger-zone">
            <summary>Опасное</summary>
            <Section title="Перенаправить агента">
              <p class="hint">Агент начнёт отправлять отчёты на другой сервер. Этот сервер перестанет его видеть.</p>
              {panelKnown === false ? (
                <>
                  <p class="hint">{PANEL_ADDRESS_MISSING}</p>
                  {onOpenAgentConnection && (
                    <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenAgentConnection}>
                      Подключение агента
                    </button>
                  )}
                </>
              ) : (
                <button type="button" class="btn btn-danger btn-wide" onClick={askRepoint}>
                  Перенаправить агента
                </button>
              )}
            </Section>
          </details>
        )}
      </div>
    </Overlay>
  )
}
