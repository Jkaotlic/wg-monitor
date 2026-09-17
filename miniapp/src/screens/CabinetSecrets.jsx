import { useEffect, useRef, useState } from 'preact/hooks'
import { addCabinetSecret, setCabinetActive, deleteCabinetSecret } from '../api.js'
import { localSheet } from '../sheet.js'
import {
  kindText,
  addSecretFields,
  addSecretReady,
  addSecretRequest,
  cabinetErrorText,
  addDoneText,
  activeDoneText,
  deleteDoneText,
  deleteSecretSheetText,
} from '../cabinetKeys.js'
import { Section } from '../ui/Section.jsx'
import { Quoted } from '../ui/Q.jsx'

// Ключи Amnezia Premium или коды HideMy.name роутера. Сам секрет вводится на
// листе (Sheet.jsx держит значение поля и стирает его при отправке); этот
// экран видит только маску. Кнопки -- по правам: добавить и выбрать
// активный могут все трое, удалить -- админ и владелец.
export function CabinetSecrets({ routerID, kind, rows = [], perms, openSheet, onChanged }) {
  const k = kindText(kind)
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')
  const alive = useRef(true)
  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  function add() {
    openSheet(
      localSheet({
        title: k.addTitle,
        body: k.addBody,
        buttonLabel: 'Проверить и сохранить',
        busyLabel: 'Проверяем…',
        fields: addSecretFields(kind),
        fieldsReady: addSecretReady(kind),
        errorText: (err) => cabinetErrorText(kind, err),
        perform: (_typed, values) => {
          const req = addSecretRequest(values)
          return addCabinetSecret(routerID, kind, req.secret, req.label)
        },
        onDone: () => onChanged(addDoneText(kind)),
      }),
    )
  }

  function makeActive(row) {
    if (busyID) return
    setBusyID(row.id)
    setError('')
    setCabinetActive(routerID, kind, row.id)
      .then(() => {
        if (alive.current) onChanged(activeDoneText(kind, row))
      })
      .catch((err) => {
        if (alive.current) setError(cabinetErrorText(kind, err))
      })
      .finally(() => {
        if (alive.current) setBusyID('')
      })
  }

  function remove(row) {
    const text = deleteSecretSheetText(kind, row)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Удалить',
        busyLabel: 'Удаляем…',
        danger: true,
        errorText: (err) => cabinetErrorText(kind, err),
        perform: () => deleteCabinetSecret(routerID, kind, row.id),
        onDone: () => onChanged(deleteDoneText(kind)),
      }),
    )
  }

  return (
    <Section title={k.sectionTitle}>
      {rows.length === 0 ? (
        <p class="state">{k.empty}</p>
      ) : (
        <ul class="card list-reset cabinet-secrets">
          {rows.map((row) => (
            <li key={row.id} class={`row cabinet-secret${row.active ? ' cabinet-secret-active' : ''}`}>
              <span class="list-row-main">
                <span class="row-title">
                  <Quoted text={row.title} />
                </span>
                {row.sub && <span class="list-row-sub">{row.sub}</span>}
              </span>
              {((!row.active && perms.manage) || perms.remove) && (
                <span class="cabinet-secret-actions">
                  {!row.active && perms.manage && (
                    <button type="button" class="btn btn-ghost btn-row" disabled={busyID === row.id} onClick={() => makeActive(row)}>
                      {busyID === row.id ? 'Сохраняем…' : 'Сделать активным'}
                    </button>
                  )}
                  {perms.remove && (
                    <button type="button" class="btn btn-ghost btn-row cabinet-danger" onClick={() => remove(row)}>
                      Удалить
                    </button>
                  )}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p class="state state-error" role="alert">
          {error}
        </p>
      )}
      {perms.manage && (
        <button type="button" class="btn btn-ghost btn-wide cabinet-add" onClick={add}>
          {k.addButton}
        </button>
      )}
    </Section>
  )
}
