// Герой -- карточка с подсветкой сверху. Холодный вариант включается, когда
// линии нет: цвет подсветки здесь несёт то же, что и метка состояния.
export function Hero({ cold = false, children }) {
  return <div class={cold ? 'hero hero-cold' : 'hero'}>{children}</div>
}
