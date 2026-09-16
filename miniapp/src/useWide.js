import { useEffect, useState } from 'preact/hooks'
import { WIDE_QUERY } from './mode.js'

function query() {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return null
  return window.matchMedia(WIDE_QUERY)
}

// useWide -- широкая ли сейчас раскладка. Меняется вместе с окном: человек
// сужает браузер -- приложение переходит в телефонную, не теряя навигации.
export function useWide() {
  const [wide, setWide] = useState(() => Boolean(query()?.matches))
  useEffect(() => {
    const mql = query()
    if (!mql) return undefined
    const onChange = (e) => setWide(Boolean(e.matches))
    setWide(Boolean(mql.matches))
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [])
  return wide
}
