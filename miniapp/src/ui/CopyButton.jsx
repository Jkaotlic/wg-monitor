import { useEffect, useRef, useState } from 'preact/hooks'
import { copyText } from '../clipboard.js'

// «Скопировать» с честным итогом: «Скопировано» на две секунды или просьба
// выделить вручную. copy подменяется в тестах.
export function CopyButton({ text, label = 'Скопировать', copy = copyText }) {
  const [state, setState] = useState('idle')
  const alive = useRef(true)
  useEffect(
    () => () => {
      alive.current = false
    },
    [],
  )

  function onClick() {
    Promise.resolve(copy(text))
      .catch(() => false)
      .then((ok) => {
        if (!alive.current) return
        setState(ok ? 'ok' : 'fail')
        setTimeout(() => {
          if (alive.current) setState('idle')
        }, 2000)
      })
  }

  return (
    <span class="copy">
      <button type="button" class="btn btn-ghost btn-row" onClick={onClick}>
        {state === 'ok' ? 'Скопировано' : label}
      </button>
      {state === 'fail' && <span class="copy-fail">Не удалось скопировать — выделите текст вручную</span>}
    </span>
  )
}
