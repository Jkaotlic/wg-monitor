import { useEffect, useState } from 'preact/hooks'
import { Section } from '../ui/Section.jsx'
import { AccessSection } from './AccessSection.jsx'
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

// Админские куски вкладки «Настройки» (v0.52): вкладка вставляет их слотами в
// родственные разделы (SettingsSections):
//   AdminRepairSections   -- «Обслуживание»: смена порта при блокировке,
//                            свободное место, пакеты по расписанию, эталонный DNS;
//   AdminSettingsSections -- «Роутер и агент»: настройки и подключение агента;
//   AdminAccessSection    -- «Люди и уведомления»: доступ;
//   AdminDangerZone       -- «Опасное»: перенаправление агента.
// Гейты ролей прежние: всё, кроме того, что сервер отдаёт всем, -- только
// админу.
//
// Парк (обновление агентов, раскатка бэкенда, добавление роутера, массовые
// действия) здесь не живёт: он про весь флот. openLayer открывает «Ход
// работы» с возвратом во вкладку.
//
// «Опасное» -- свёрнутый раздел вкладки: перенаправление уводит роутер с
// этого сервера, и случайно раскрыть его пролистыванием нельзя. Запуск ведёт
// на «Ход работы» через openLayer (возврат -- сюда же).
export function AdminRepairSections({ isAdmin = false, onOpenDNSReset, onOpenPackages, onOpenPorthop, onOpenSpace }) {
  return (
    <>
      {/* v0.57: смена порта и место -- только админ (сервер ответит
          остальным 404); версию агента экраны проверяют сами. */}
      {isAdmin && onOpenPorthop && (
        <Section title="Смена порта при блокировке">
          <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenPorthop}>
            Открыть смену порта
          </button>
          <p class="hint">
            Если фильтр провайдера обрывает VPN-туннель, роутер сам сменит исходящий порт, и VPN-туннель
            снова поднимется.
          </p>
        </Section>
      )}

      {isAdmin && onOpenSpace && (
        <Section title="Свободное место">
          <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenSpace}>
            Открыть свободное место
          </button>
          <p class="hint">Сколько места на накопителе Entware и чем оно занято, очистка сейчас и по расписанию.</p>
        </Section>
      )}

      {isAdmin && onOpenPackages && (
        <Section title="Пакеты по расписанию">
          <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenPackages}>
            Открыть пакеты по расписанию
          </button>
          <p class="hint">Обновление пакетов Entware по расписанию на самом роутере.</p>
        </Section>
      )}

      {/* Эталонный DNS (бывш. «Сброс DNS») -- вход только у админа (радиус
          router-global, сервер ответит остальным 404). Экран сам проверяет
          версию агента и начинает с предпросмотра. */}
      {isAdmin && onOpenDNSReset && (
        <Section title="Эталонный DNS">
          <button type="button" class="btn btn-ghost btn-wide" onClick={onOpenDNSReset}>
            Открыть эталонный DNS
          </button>
          <p class="hint">
            Заменить DNS-серверы роутера эталонными — сначала роутер проверит, что они отвечают, а экран
            покажет, что изменится.
          </p>
        </Section>
      )}
    </>
  )
}

export function AdminSettingsSections({ routerID, isAdmin = false, openSheet, onOpenAgentConfig, onOpenAgentConnection }) {
  return (
    <>
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
    </>
  )
}

// «Доступ» -- раздел «Люди и уведомления» (v0.52), только админ: сервер
// проверяет роль сам (miniappRequireAdmin).
export function AdminAccessSection({ routerID, openSheet }) {
  return (
    <div id="mg-access" class="manage-anchor-target">
      <AccessSection routerID={routerID} openSheet={openSheet} />
    </div>
  )
}

export function AdminDangerZone({ routerID, routerName = '', isAdmin = false, openSheet, openLayer, onOpenAgentConnection }) {
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
    <>
      {isAdmin && routerName && (
        <>
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
        </>
      )}
    </>
  )
}
