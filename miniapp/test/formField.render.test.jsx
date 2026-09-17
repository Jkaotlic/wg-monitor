// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { TextField, SelectField, ChoiceList } from '../src/ui/FormField.jsx'
import { CopyButton } from '../src/ui/CopyButton.jsx'

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }

describe('TextField', () => {
  // Браузер знает свойство spellcheck, jsdom -- нет. Без него Preact пошёл бы
  // по ветке атрибутов, которой в настоящем приложении нет.
  if (!('spellcheck' in HTMLElement.prototype)) {
    Object.defineProperty(HTMLElement.prototype, 'spellcheck', { value: true, writable: true, configurable: true })
  }

  it('пароль: type=password, без автозаполнения и проверки орфографии', async () => {
    const got = []
    const root = await mount(<TextField id="f-root" label="Пароль root" type="password" value="" onInput={(v) => got.push(v)} hint="один раз" />)
    const input = root.querySelector('#f-root')
    expect(input.getAttribute('type')).toBe('password')
    // new-password: иначе браузер подставит сохранённый пароль сайта.
    expect(input.getAttribute('autocomplete')).toBe('new-password')
    expect(input.getAttribute('autocapitalize')).toBe('off')
    // В jsdom нет свойства spellcheck (браузеры его знают): Preact тогда
    // снимает атрибут у false, и проверять нечего. Свойство подставлено
    // выше, как в браузере -- значит, false дошёл до поля.
    expect(input.spellcheck).toBe(false)
    expect(root.querySelector('label').getAttribute('for')).toBe('f-root')
    expect(root.querySelector('.field-hint').textContent).toBe('один раз')
    await act(async () => {
      input.value = 's3cret'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(got).toEqual(['s3cret'])
    cleanup(root)
  })

  it('по умолчанию -- текст', async () => {
    const root = await mount(<TextField id="f-nick" label="Имя" value="dacha" onInput={() => {}} />)
    expect(root.querySelector('#f-nick').getAttribute('type')).toBe('text')
    expect(root.querySelector('#f-nick').value).toBe('dacha')
    expect(root.querySelector('.field-hint')).toBe(null)
    cleanup(root)
  })
})

describe('SelectField', () => {
  it('варианты и выбор', async () => {
    const got = []
    const options = [{ value: 'web', label: 'Вход в веб' }, { value: 'none', label: 'Без входа' }]
    const root = await mount(<SelectField id="f-auth" label="Вход" value="web" options={options} onChange={(v) => got.push(v)} />)
    const select = root.querySelector('#f-auth')
    expect([...select.options].map((o) => o.textContent)).toEqual(['Вход в веб', 'Без входа'])
    await act(async () => {
      select.value = 'none'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(got).toEqual(['none'])
    cleanup(root)
  })
})

describe('ChoiceList', () => {
  it('радиогруппа карточек', async () => {
    const got = []
    const options = [{ value: 'install', title: 'Установить', sub: 'сейчас' }, { value: 'token', title: 'Токен' }]
    const root = await mount(<ChoiceList label="Как добавить" value="token" options={options} onChange={(v) => got.push(v)} />)
    const group = root.querySelector('[role="radiogroup"]')
    expect(group.getAttribute('aria-label')).toBe('Как добавить')
    const radios = [...root.querySelectorAll('[role="radio"]')]
    expect(radios.map((r) => r.getAttribute('aria-checked'))).toEqual(['false', 'true'])
    expect(radios[1].classList.contains('choice-on')).toBe(true)
    expect(radios[0].querySelector('.choice-sub').textContent).toBe('сейчас')
    await act(async () => radios[0].click())
    expect(got).toEqual(['install'])
    cleanup(root)
  })
})

describe('CopyButton', () => {
  it('успех -- «Скопировано»', async () => {
    const copied = []
    const root = await mount(<CopyButton text="tok-1" copy={async (t) => { copied.push(t); return true }} />)
    const btn = root.querySelector('button')
    expect(btn.textContent).toBe('Скопировать')
    await act(async () => btn.click())
    await flush()
    expect(copied).toEqual(['tok-1'])
    expect(btn.textContent).toBe('Скопировано')
    cleanup(root)
  })

  it('неудача -- просьба выделить вручную', async () => {
    const root = await mount(<CopyButton text="tok-1" label="Скопировать команду" copy={async () => false} />)
    expect(root.querySelector('button').textContent).toBe('Скопировать команду')
    await act(async () => root.querySelector('button').click())
    await flush()
    expect(root.querySelector('.copy-fail').textContent).toBe('Не удалось скопировать — выделите текст вручную')
    cleanup(root)
  })
})

describe('CopyButton: один таймер', () => {
  it('повторное нажатие не даёт первому таймеру погасить «Скопировано» раньше срока', async () => {
    vi.useFakeTimers()
    try {
      const root = await mount(<CopyButton text="t" copy={async () => true} />)
      const btn = root.querySelector('button')
      await act(async () => btn.click())
      await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
      await act(async () => btn.click())
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      expect(btn.textContent).toBe('Скопировано')
      await act(async () => { await vi.advanceTimersByTimeAsync(1100) })
      expect(btn.textContent).toBe('Скопировать')
      cleanup(root)
    } finally {
      vi.useRealTimers()
    }
  })
})
