import { useEffect, useRef, useState } from 'preact/hooks'
import { Section } from '../ui/Section.jsx'
import { openPremiumFiles, downloadPremiumFile } from '../api.js'

export function AmneziaDashboard({ routerID, rows = [], perms = {}, onChanged }) {
  const [key, setKey] = useState('')
  const [selectedKey, setSelectedKey] = useState('')
  const [session, setSession] = useState(null)
  const [country, setCountry] = useState('')
  const [declared, setDeclared] = useState('ru')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [file, setFile] = useState(null)
  const [accountStale, setAccountStale] = useState(false)
  const alive = useRef(true)
  const inFlight = useRef(false)
  const fileURL = useRef('')
  const canDownload = perms.remove === true

  useEffect(() => () => {
    alive.current = false
    if (fileURL.current) URL.revokeObjectURL(fileURL.current)
  }, [])

  function clearFile() {
    if (fileURL.current) URL.revokeObjectURL(fileURL.current)
    fileURL.current = ''
    setFile(null)
  }

  function changeKey(value) {
    setSelectedKey(value)
    setSession(null)
    setCountry('')
    clearFile()
    setError('')
  }

  async function open(event) {
    event.preventDefault()
    if (inFlight.current || !canDownload) return
    inFlight.current = true
    setBusy('account')
    setError('')
    setSession(null)
    clearFile()
    const submittedKey = key.trim()
    setKey('')
    try {
      const result = await openPremiumFiles(routerID, submittedKey ? { vpn_key: submittedKey } : { key_id: selectedKey })
      if (!alive.current) return
      setSession(result)
      setAccountStale(false)
      setSelectedKey(result.key_id)
      setCountry(result.account?.available_countries?.[0]?.server_country_code || '')
      if (submittedKey) onChanged?.('Ключ Amnezia проверен и сохранён.')
    } catch (err) {
      if (alive.current) setError(err.serverMessage || 'Не удалось открыть подписку Amnezia. Проверьте ключ и повторите.')
    } finally {
      inFlight.current = false
      if (alive.current) setBusy('')
    }
  }

  async function generate() {
    if (inFlight.current || !session || !country || !canDownload) return
    inFlight.current = true
    setBusy('download')
    setError('')
    clearFile()
    try {
      const result = await downloadPremiumFile(routerID, session.key_id, country, declared)
      if (!alive.current) return
      if (!result.conf || !result.filename) throw new Error('invalid response')
      fileURL.current = URL.createObjectURL(new Blob([result.conf], { type: 'text/plain;charset=utf-8' }))
      setFile({ url: fileURL.current, name: result.filename })
      setAccountStale(true)
      // Refreshing the account cannot generate a second file. Keep the ready
      // file available even if the subscription refresh fails.
      try {
        const refreshed = await openPremiumFiles(routerID, { key_id: session.key_id })
        if (alive.current) {
          setSession(refreshed)
          setAccountStale(false)
        }
      } catch {
        // The download succeeded; show the stale status beside the file.
      }
    } catch (err) {
      if (alive.current) setError(err.serverMessage || 'Ответ о выпуске не получен. Откройте подписку заново и проверьте страну перед повтором.')
    } finally {
      inFlight.current = false
      if (alive.current) setBusy('')
    }
  }

  const account = session?.account
  const issued = new Set((account?.issued_configs || []).filter((item) => item.source_type === 'country_config').map((item) => item.server_country_code))
  return (
    <Section title="Amnezia Premium">
      <p class="hint">Встроенный выпуск использует внутренний API кабинета Amnezia. Ранее автор awg-manager предупреждал о риске блокировки подписки при таком доступе. Новый конфиг может занять место в подписке.</p>
      {!canDownload ? <p class="state">Выпуск и скачивание доступны владельцу роутера и администратору.</p> : <>
        <form onSubmit={open}>
          {rows.length > 0 && <div class="field"><label for={`premium-saved-${routerID}`}>Сохранённый ключ</label>
            <select id={`premium-saved-${routerID}`} aria-label="Сохранённый ключ Amnezia" value={selectedKey} disabled={Boolean(busy)} onChange={(event) => changeKey(event.currentTarget.value)}>
              <option value="">Активный ключ</option>
              {rows.map((row) => <option key={row.id} value={row.id}>{row.label} {row.mask}</option>)}
            </select>
          </div>}
          <div class="field"><label for={`premium-key-${routerID}`}>Ключ Amnezia Premium</label>
            <input id={`premium-key-${routerID}`} type="password" autoComplete="off" maxLength={8192} placeholder="vpn://…" value={key} disabled={Boolean(busy)} onInput={(event) => { changeKey(selectedKey); setKey(event.currentTarget.value) }} />
          </div>
          <p class="hint">Введите новый ключ или откройте подписку сохранённым ключом.</p>
          <button type="submit" class="btn btn-primary btn-wide" disabled={Boolean(busy) || (!key.trim() && rows.length === 0)}>{busy === 'account' ? 'Проверяем ключ…' : key.trim() ? 'Проверить и сохранить' : 'Открыть подписку'}</button>
        </form>
        {account && <div>
          {accountStale && <p class="hint" role="status">Конфиг готов. Данные подписки показаны до выпуска — обновите их кнопкой «Открыть подписку».</p>}
          <p class="hint">Занято мест: {account.active_device_count} из {account.max_device_count}. {account.subscription_end_date ? `Подписка до ${account.subscription_end_date.slice(0, 10)}.` : ''}</p>
          <div class="field"><label for={`premium-country-${routerID}`}>Страна VPN</label>
            <select id={`premium-country-${routerID}`} aria-label="Страна VPN" value={country} disabled={Boolean(busy)} onChange={(event) => { setCountry(event.currentTarget.value); clearFile() }}>
              {(account.available_countries || []).map((item) => <option key={item.server_country_code} value={item.server_country_code}>{item.server_country_name || item.server_country_code}{issued.has(item.server_country_code) ? ' — уже выпущена' : ''}</option>)}
            </select>
          </div>
          <div class="field"><label for={`premium-region-${routerID}`}>Откуда подключаетесь</label>
            <select id={`premium-region-${routerID}`} aria-label="Откуда подключаетесь" value={declared} disabled={Boolean(busy)} onChange={(event) => { setDeclared(event.currentTarget.value); clearFile() }}>
              <option value="ru">Россия</option><option value="ag">Другая страна</option>
            </select>
          </div>
          <p class="hint">Файл содержит приватный ключ. Для роутера нужна поддержка AWG 3.1. AWG Manager может отклонить конфиг Amnezia Premium.</p>
          <button type="button" class="btn btn-primary btn-wide" disabled={Boolean(busy) || !country} onClick={generate}>{busy === 'download' ? 'Выпускаем конфиг…' : issued.has(country) ? 'Получить .conf' : 'Выпустить .conf'}</button>
          {file && <p role="status"><a class="btn btn-primary btn-wide" href={file.url} download={file.name}>Скачать готовый .conf</a></p>}
        </div>}
      </>}
      {error && <p class="state state-error" role="alert">{error}</p>}
    </Section>
  )
}
