import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { useOnClose } from '../useOnClose.js'
import { fetchCabinets, fetchVPNAccounts, fetchRouterSettings, fetchSelfhosted, fetchAwg3Issuable } from '../api.js'
import { cabinetTabs, pickTab, cabinetPerms, secretRows, VPN_PROVIDER, CABINET_TEXTS } from '../cabinetKeys.js'
import { Overlay } from '../ui/Overlay.jsx'
import { SegmentTabs } from '../ui/SegmentTabs.jsx'
import { AmneziaDashboard } from './AmneziaDashboard.jsx'
import { CabinetSecrets } from './CabinetSecrets.jsx'
import { CabinetOptions } from './CabinetOptions.jsx'
import { CabinetAwg3 } from './CabinetAwg3.jsx'
import { CabinetSelfhosted } from './CabinetSelfhosted.jsx'
import { CabinetIssue } from './CabinetIssue.jsx'

// «Откуда взять конфиг»: Amnezia · HideMy · Свой сервер (админ). Ключи и коды
// вводятся здесь (цикл 3 «бот без слеш-команд») -- на листе, полем-паролем;
// экран видит только маску. Выпуск -- как раньше: клиент передаёт выбор,
// конфиг сервер кладёт в команду агенту сам.
//
// Кнопки рисуются по роли из настроек роутера; граница доступа -- сервер.
export function CabinetScreen({ routerID, routerName = '', asleep = false, openSheet, onClose, onIssued, layer = 'cabinet', layerParams = {}, initialTab = 'amnezia', openLayer, closeLayer, onPin, pinned = false }) {
  const [cabinets, setCabinets] = useState(null)
  const [loadError, setLoadError] = useState('')
  const [accounts, setAccounts] = useState(null)
  const [role, setRole] = useState('')
  const [instances, setInstances] = useState(null)
  const [instancesError, setInstancesError] = useState('')
  // Панели awg3, с которых можно выпустить на этот роутер (null -- грузятся).
  const [awg3Panels, setAwg3Panels] = useState(null)
  const [awg3Failed, setAwg3Failed] = useState(false)
  const [tab, setTab] = useState(initialTab || 'amnezia')
  // Выпуск -- слой навигации (cabinetissue): «назад» Telegram закрывает его,
  // а выбранный вариант живёт в параметрах слоя (v0.52).
  const pending = layer === 'cabinetissue' ? layerParams.pending ?? null : null
  const pick = (p) => openLayer?.('cabinetissue', { pending: p })
  const [notice, setNotice] = useState('')
  // Права не прочитались: без роли экран молча стал бы «только чтение».
  const [roleError, setRoleError] = useState(false)

  const alive = useRef(true)
  useLayoutEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  function load() {
    setLoadError('')
    fetchCabinets(routerID)
      .then((data) => {
        if (!alive.current) return
        setCabinets(data ?? {})
        if (data?.selfhosted?.available === true) {
          setInstancesError('')
          fetchSelfhosted()
            .then((resp) => {
              if (alive.current) setInstances(resp?.instances ?? [])
            })
            .catch(() => {
              if (alive.current) setInstancesError(CABINET_TEXTS.instancesError)
            })
        }
      })
      .catch((err) => {
        if (!alive.current) return
        // Кабинеты не настроены -- состояние сервера, и он сам его называет.
        setLoadError(err?.code === 'cabinets_not_configured' && err.serverMessage ? err.serverMessage : CABINET_TEXTS.loadError)
      })
    loadAwg3()
    fetchVPNAccounts(routerID)
      .then((resp) => {
        if (!alive.current) return
        const byProvider = {}
        for (const acc of resp?.accounts ?? []) byProvider[acc.provider] = acc
        setAccounts(byProvider)
      })
      .catch(() => {
        if (alive.current) setAccounts({})
      })
  }

  // Ошибка списка панелей -- вкладка «Панель VPN-сервера» с «Повторить», а не
  // тишина (хвост v0.51): иначе владелец с допуском решил бы, что панелей нет.
  function loadAwg3() {
    setAwg3Failed(false)
    setAwg3Panels(null)
    fetchAwg3Issuable(routerID)
      .then((resp) => {
        if (alive.current) setAwg3Panels(resp?.panels ?? [])
      })
      .catch(() => {
        if (!alive.current) return
        setAwg3Panels([])
        setAwg3Failed(true)
      })
  }

  useEffect(() => {
    setCabinets(null)
    setAccounts(null)
    setInstances(null)
    setAwg3Panels(null)
    setNotice('')
    setRole('')
    loadRole()
    load()
  }, [routerID])

  function loadRole() {
    setRoleError(false)
    fetchRouterSettings(routerID)
      .then((s) => {
        if (alive.current) setRole(s?.role ?? '')
      })
      .catch(() => {
        if (alive.current) setRoleError(true)
      })
  }

  // Уход с экрана выпуска: подписка могла измениться (занятые места,
  // выпущенные страны) -- кабинет перечитывается.
  // Закрепление снимает сам CabinetIssue (onBusy(false)), поэтому здесь его не
  // трогаем: иначе своя «назад» отпускала бы слой посреди выпуска.
  function leaveIssue() {
    closeLayer?.()
  }

  // «Назад» Telegram и кнопка слоя закрывают выпуск одинаково: перечитать
  // подписку (занятые места) здесь, а не в leaveIssue, который «назад»
  // Telegram обходит.
  useOnClose(layer === 'cabinetissue', () => {
    setAccounts(null)
    load()
  })

  function issued() {
    onIssued?.()
    load()
  }

  // Ключ добавлен, выбран, удалён или страна отозвана: итог -- строкой над
  // вкладкой, кабинеты и подписка -- заново (активный ключ мог смениться).
  function changed(text) {
    setNotice(text)
    setAccounts(null)
    load()
  }

  const tabs = cabinetTabs(cabinets, awg3Panels, { awg3Failed })
  const current = pickTab(tabs, tab)
  const perms = cabinetPerms(role)
  const title = routerName ? `${CABINET_TEXTS.title} «${routerName}»` : CABINET_TEXTS.title

  function accountFor(kind) {
    if (accounts == null) return null
    return accounts[VPN_PROVIDER[kind]] ?? { provider: VPN_PROVIDER[kind], disabled: kind === 'amnezia', connected: false, note: CABINET_TEXTS.accountError }
  }

  function body() {
    if (pending && (pending.provider !== 'amnezia' || pending.keyID)) {
      return (
        <CabinetIssue
          key={`${pending.provider}:${pending.option.id}`}
          routerID={routerID}
          asleep={asleep}
          pending={pending}
          perms={perms}
          openSheet={openSheet}
          onIssued={issued}
          onBusy={(busy) => onPin?.(busy)}
          onBackToList={leaveIssue}
        />
      )
    }
    const rows = cabinets && current !== 'selfhosted' && current !== 'awg3' ? secretRows(cabinets, current) : []
    const account = accountFor(current)
    const externalPremium = current === 'amnezia'
    return (
      <>
        <SegmentTabs
          label={CABINET_TEXTS.title}
          tabs={tabs}
          value={current}
          onChange={(id) => {
            setTab(id)
            setNotice('')
          }}
        />
        {notice && (
          <p class="hint cabinet-notice" role="status">
            {notice}
          </p>
        )}
        {externalPremium && (
          <>
            <AmneziaDashboard key={routerID} routerID={routerID} rows={rows} perms={perms} onChanged={changed} onPick={pick} />
            {perms.manage && openLayer && (
              <button type="button" class="btn btn-ghost btn-wide" onClick={() => openLayer('confimport', { returnTo: 'cabinet', returnParams: { ...layerParams, tab: current } })}>
                Загрузить скачанный .conf
              </button>
            )}
          </>
        )}
        {current === 'awg3' ? (
          <CabinetAwg3 panels={awg3Panels} error={awg3Failed ? CABINET_TEXTS.awg3LoadError : ''} onRetry={loadAwg3} onPick={pick} admin={perms.admin} />
        ) : !cabinets && !loadError ? (
          <p class="state">{CABINET_TEXTS.loading}</p>
        ) : loadError ? (
          <p class="state state-error">{loadError}</p>
        ) : externalPremium ? (
          rows.length > 0 && perms.remove ? (
            <details class="checks-spoiler">
              <summary class="checks-spoiler-summary">Ранее сохранённые ключи ({rows.length})</summary>
              <CabinetSecrets
                routerID={routerID}
                kind="amnezia"
                rows={rows}
                perms={perms}
                openSheet={openSheet}
                onChanged={changed}
                disabledNote="Для выпуска и установки Amnezia Premium выберите подписку и страну в форме выше."
              />
            </details>
          ) : account?.disabled && account.note ? <p class="hint">{account.note}</p> : null
        ) : current === 'selfhosted' ? (
          <CabinetSelfhosted instances={instances} error={instancesError} onPick={pick} />
        ) : (
          <>
            <CabinetSecrets
              key={current}
              routerID={routerID}
              kind={current}
              rows={rows}
              perms={perms}
              openSheet={openSheet}
              onChanged={changed}
              disabledNote={account?.disabled === true ? account.note || CABINET_TEXTS.disabled : ''}
            />
            {/* Кабинет выключен целиком (v0.61.3: «Amnezia Premium») -- выпускать
                нечего, причина уже сказана над ключами. */}
            {rows.length > 0 && account?.disabled !== true && (
              <CabinetOptions
                routerID={routerID}
                routerName={routerName}
                kind={current}
                account={account}
                perms={perms}
                openSheet={openSheet}
                onPick={pick}
                onChanged={changed}
              />
            )}
          </>
        )}
      </>
    )
  }

  return (
    <Overlay title={title} backLabel={pending || layerParams.returnTo ? 'Назад' : 'VPN-туннели'} onBack={pinned ? undefined : pending ? leaveIssue : onClose}>
      <div class="screen cabinet">
        {roleError && (
          <div class="card cabinet-role-error">
            <p class="state state-error" role="alert">
              {CABINET_TEXTS.roleError}
            </p>
            <button type="button" class="btn btn-ghost btn-wide" onClick={loadRole}>
              Повторить
            </button>
          </div>
        )}
        {body()}
      </div>
    </Overlay>
  )
}
