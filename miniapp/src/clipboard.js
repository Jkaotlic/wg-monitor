// Копирование в буфер обмена. navigator.clipboard есть только в защищённом
// контексте и не во всех WebView Telegram; запасной путь -- выделение во
// временном textarea и execCommand('copy'). Итог -- true/false: экран говорит
// «Скопировано» или «выделите вручную», а не молчит.
//
// env подменяется в тестах; в приложении это globalThis.
export async function copyText(text, env = globalThis) {
  const value = typeof text === 'string' ? text : String(text ?? '')
  if (!value) return false
  const clip = env?.navigator?.clipboard
  if (clip && typeof clip.writeText === 'function') {
    try {
      await clip.writeText(value)
      return true
    } catch {
      // отказ разрешения -- пробуем запасной путь
    }
  }
  const doc = env?.document
  if (!doc || typeof doc.createElement !== 'function' || typeof doc.execCommand !== 'function' || !doc.body) return false
  const area = doc.createElement('textarea')
  area.value = value
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  doc.body.appendChild(area)
  area.select()
  let ok = false
  try {
    ok = Boolean(doc.execCommand('copy'))
  } catch {
    ok = false
  }
  area.remove()
  return ok
}
