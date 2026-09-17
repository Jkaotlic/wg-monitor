import { useEffect, useRef, useState } from 'preact/hooks'
import { fetchCabinets, fetchVPNAccounts, fetchRouterSettings, fetchSelfhosted } from '../api.js'
import { cabinetTabs, pickTab, cabinetPerms, secretRows, VPN_PROVIDER, CABINET_TEXTS } from '../cabinetKeys.js'
import { Overlay } from '../ui/Overlay.jsx'
import { SegmentTabs } from '../ui/SegmentTabs.jsx'
import { CabinetSecrets } from './CabinetSecrets.jsx'
import { CabinetOptions } from './CabinetOptions.jsx'
import { CabinetSelfhosted } from './CabinetSelfhosted.jsx'
import { CabinetIssue } from './CabinetIssue.jsx'

// Кабинеты VPN роутера: Amnezia · HideMy · Свой сервер (админ). Ключи и коды
// вводятся здесь (цикл 3 «бот без слеш-команд») -- на листе, полем-паролем;
// экран видит только маску. Выпуск -- как раньше: клиент передаёт выбор,
// конфиг сервер кладёт в команду агенту сам.
//
// Кнопки рисуются по роли из настроек роутера; граница доступа -- сервер.
export function CabinetScreen({ routerID, routerName = '', asleep = false, openSheet, onClose, onIssued }) {
  const [cabinets, setCabinets] = useState(null)
  const [loadError, setLoadError] = useState('')
  const [accounts, setAccounts] = useState(null)
  const [role, setRole] = useState('')
  const [instances, setInstances] = useState(null)
  const [instancesError, setInstancesError] = useState('')
  const [tab, setTab] = useState('amnezia')
  const [pending, setPending] = useState(null)
  const [notice, setNotice] = useState('')

  const alive = useRef(true)
  useEffect(
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
      .catch(() => {
        if (alive.current) setLoadError(CABINET_TEXTS.loadError)
      })
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

  useEffect(() => {
    setCabinets(null)
    setAccounts(null)
    setInstances(null)
    setPending(null)
    setNotice('')
    setRole('')
    fetchRouterSettings(routerID)
      .then((s) => {
        if (alive.current) setRole(s?.role ?? '')
      })
      .catch(() => {})
    load()
  }, [routerID])

  // Ключ добавлен, выбран, удалён или страна отозвана: итог -- строкой над
  // вкладкой, кабинеты и подписка -- заново (активный ключ мог смениться).
  function changed(text) {
    setNotice(text)
    setAccounts(null)
    load()
  }

  const tabs = cabinetTabs(cabinets)
  const current = pickTab(tabs, tab)
  const perms = cabinetPerms(role)
  const title = routerName ? `${CABINET_TEXTS.title} «${routerName}»` : CABINET_TEXTS.title

  function accountFor(kind) {
    if (accounts == null) return null
    return accounts[VPN_PROVIDER[kind]] ?? { provider: VPN_PROVIDER[kind], connected: false, note: CABINET_TEXTS.accountError }
  }

  function body() {
    if (loadError) return <p class="state state-error">{loadError}</p>
    if (!cabinets) return <p class="state">{CABINET_TEXTS.loading}</p>
    if (pending) {
      return (
        <CabinetIssue
          key={`${pending.provider}:${pending.option.id}`}
          routerID={routerID}
          asleep={asleep}
          pending={pending}
          perms={perms}
          openSheet={openSheet}
          onIssued={onIssued}
          onBackToList={() => setPending(null)}
        />
      )
    }
    const rows = current === 'selfhosted' ? [] : secretRows(cabinets, current)
    return (
      <>
        <SegmentTabs
          label="Кабинеты"
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
        {current === 'selfhosted' ? (
          <CabinetSelfhosted instances={instances} error={instancesError} onPick={setPending} />
        ) : (
          <>
            <CabinetSecrets key={current} routerID={routerID} kind={current} rows={rows} perms={perms} openSheet={openSheet} onChanged={changed} />
            {rows.length > 0 && (
              <CabinetOptions
                routerID={routerID}
                routerName={routerName}
                kind={current}
                account={accountFor(current)}
                perms={perms}
                openSheet={openSheet}
                onPick={setPending}
                onChanged={changed}
              />
            )}
          </>
        )}
      </>
    )
  }

  return (
    <Overlay title={title} backLabel={pending ? 'Назад' : 'VPN-туннели'} onBack={pending ? () => setPending(null) : onClose}>
      <div class="screen cabinet">{body()}</div>
    </Overlay>
  )
}
