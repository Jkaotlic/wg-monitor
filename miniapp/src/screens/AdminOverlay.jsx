import { useEffect, useState } from 'preact/hooks'
import { Overlay } from '../ui/Overlay.jsx'
import { Section } from '../ui/Section.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { AccessSection } from './AccessSection.jsx'
import { fetchFleet, createWebLink } from '../api.js'
import { openExternal } from '../telegram.js'
import {
  backendRow,
  fleetHeadline,
  fleetRouterRows,
  notifyGapLines,
  watchdogLine,
  webLinkLines,
} from '../fleetAdmin.js'

// Администрирование: парк целиком и доступы к этому роутеру.
//
// Экран парка читающий: в этой версии на нём не делается ничего, кроме
// выдачи ссылки на веб-управление. Кнопок-заглушек здесь по-прежнему нет --
// кнопка, которая ничего не делает, хуже её отсутствия.
//
// Парк видит только админ. Сервер отвечает 404 всем остальным, и этот
// признак -- подсказка интерфейсу, а не граница доступа.
export function AdminOverlay({ routerID, isAdmin = false, onClose, openSheet }) {
  const [fleet, setFleet] = useState(null)
  const [fleetError, setFleetError] = useState(null)

  const [linkBusy, setLinkBusy] = useState(false)
  const [linkLines, setLinkLines] = useState([])
  const [linkError, setLinkError] = useState(null)

  useEffect(() => {
    if (!isAdmin) return
    fetchFleet()
      .then((data) => {
        setFleet(data)
        setFleetError(null)
      })
      .catch(() => setFleetError('Не удалось прочитать сводку парка.'))
  }, [isAdmin])

  // Ссылку выдаёт сервер, он же говорит словами про срок и лимит: своих
  // текстов про «12 часов» приложение не сочиняет.
  function openInBrowser() {
    setLinkBusy(true)
    setLinkError(null)
    createWebLink()
      .then((grant) => {
        setLinkLines(webLinkLines(grant))
        openExternal(grant.url)
      })
      .catch((err) => setLinkError(err?.serverMessage || 'Не удалось выдать ссылку.'))
      .finally(() => setLinkBusy(false))
  }

  const rows = fleet ? fleetRouterRows(fleet) : []
  const gaps = fleet ? notifyGapLines(fleet) : []
  const watchdog = fleet ? watchdogLine(fleet) : ''
  const backend = fleet ? backendRow(fleet) : null

  return (
    <Overlay title="Обслуживание и доступы" backLabel="Роутер" onBack={onClose}>
      <div class="screen">
        {isAdmin && (
          <Section title="Парк">
            {fleetError ? (
              <p class="state state-error">{fleetError}</p>
            ) : !fleet ? (
              <p class="state">Загрузка…</p>
            ) : (
              <>
                <p class="router-lastseen">{fleetHeadline(fleet)}</p>

                <div class="card">
                  <DataRow title="Бэкенд" value={backend.value} valueSub={backend.sub} />
                </div>

                {rows.length > 0 && (
                  <div class="card">
                    {rows.map((row) => (
                      <div key={row.id}>
                        <DataRow title={row.name} value={row.state} valueSub={row.sub} />
                        {(row.versions || row.hint) && (
                          <p class="hint">{[row.versions, row.hint].filter(Boolean).join(' · ')}</p>
                        )}
                      </div>
                    ))}
                  </div>
                )}

                {gaps.length > 0 && (
                  <>
                    <h3 class="row-title">Уведомления</h3>
                    {gaps.map((line) => (
                      <p class="hint" key={line}>
                        {line}
                      </p>
                    ))}
                  </>
                )}

                {watchdog && <p class="hint">Сторож парка: {watchdog}</p>}

                <button type="button" class="btn btn-ghost btn-wide" disabled={linkBusy} onClick={openInBrowser}>
                  {linkBusy ? 'Выдаём ссылку…' : 'Открыть в браузере'}
                </button>
                {linkLines.map((line) => (
                  <p class="hint" key={line}>
                    {line}
                  </p>
                ))}
                {linkError && <p class="state state-error">{linkError}</p>}
              </>
            )}
          </Section>
        )}

        <AccessSection routerID={routerID} openSheet={openSheet} />
        <p class="muted admin-note">
          Подключение новых роутеров, обслуживание пакетов и бэкапы пока живут в браузерном
          дашборде — его открывает кнопка выше.
        </p>
      </div>
    </Overlay>
  )
}
