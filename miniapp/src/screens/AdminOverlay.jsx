import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { AccessSection } from './AccessSection.jsx'
import { ParkSection } from './ParkSection.jsx'
import { repointRouterAgent } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  JOB_SECRET_NOTE,
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
export function AdminOverlay({ routerID, routerName = '', isAdmin = false, onClose, openSheet, openLayer, onOpenAgentConfig, onOpenAgentConnection, onOpenDNSReset, onOpenRouter }) {
  const router = { id: routerID, nickname: routerName }

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
        {isAdmin && <ParkSection openSheet={openSheet} onOpenRouter={onOpenRouter} currentID={routerID} openLayer={openLayer} />}

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

        <AccessSection routerID={routerID} openSheet={openSheet} />

        {isAdmin && routerName && (
          <details class="danger-zone">
            <summary>Опасное</summary>
            <Section title="Перенаправить агента">
              <p class="hint">Агент начнёт отправлять отчёты на другой сервер. Этот сервер перестанет его видеть.</p>
              <button type="button" class="btn btn-danger btn-wide" onClick={askRepoint}>
                Перенаправить агента
              </button>
            </Section>
          </details>
        )}
      </div>
    </Overlay>
  )
}
