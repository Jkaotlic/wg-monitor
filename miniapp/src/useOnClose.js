import { useEffect, useRef } from 'preact/hooks'

// useOnClose -- вызвать fn, когда слой закрылся (open: true → false). Нужен
// вкладке VPN-туннелей на телефоне: кабинет открыт поверх неё, вкладка не
// размонтирована, и выпущенный VPN-туннель она иначе не увидит. На широкой
// раскладке вкладка монтируется заново и перечитывает всё сама.
export function useOnClose(open, fn) {
  const prev = useRef(open)
  const fnRef = useRef(fn)
  fnRef.current = fn
  useEffect(() => {
    if (prev.current && !open) fnRef.current?.()
    prev.current = open
  }, [open])
}
