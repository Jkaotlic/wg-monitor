import { revokeAmneziaSlot } from '../api.js'
import { localSheet } from '../sheet.js'
import { accountSummary, optionRows } from '../cabinet.js'
import { VPN_PROVIDER, CABINET_TEXTS, revokeSheetText, revokeDoneText, revokeErrorText } from '../cabinetKeys.js'
import { Section } from '../ui/Section.jsx'
import { Quoted } from '../ui/Q.jsx'

// Подписка и то, что из неё можно выпустить. Выпущенная страна Amnezia
// отзывается здесь же (админ и владелец) набором имени роутера: отзыв
// ломает конфиг везде, где он стоит, и обратно его не вернуть.
//
// Строка варианта -- две кнопки рядом, а не кнопка в кнопке: выбор на
// выпуск и «Отозвать» -- разные действия.
export function CabinetOptions({ routerID, routerName, kind, account, perms, openSheet, onPick, onChanged }) {
  if (!account) return <p class="state">{CABINET_TEXTS.accountLoading}</p>

  const revocable = kind === 'amnezia' && perms.revoke && Boolean(routerName)
  const summary = accountSummary(account, { canRevoke: revocable })
  const options = account.connected ? optionRows(account) : []

  function revoke(option) {
    const text = revokeSheetText(option, routerName)
    openSheet(
      localSheet({
        title: text.title,
        body: text.body,
        buttonLabel: 'Отозвать',
        busyLabel: 'Отзываем…',
        danger: true,
        confirmPhrase: routerName,
        errorText: revokeErrorText,
        perform: (typed) => revokeAmneziaSlot(routerID, option.id, typed),
        onDone: () => onChanged(revokeDoneText(option)),
      }),
    )
  }

  return (
    <Section title={summary.title}>
      <div class="card">
        {summary.lines.map((line) => (
          <p key={line} class="traffic-detail">
            {line}
          </p>
        ))}
        {/* Подвал отделяется линией от того, что над ним; без строк над ним
            это просто фраза в карточке. */}
        {(!summary.canIssue || summary.full) && <p class={summary.lines.length ? 'card-foot' : 'traffic-detail'}>{summary.reason}</p>}
      </div>
      {options.length > 0 && (
        <ul class="card list-reset cabinet-options">
          {options.map((o) => (
            <li key={o.id} class="row cabinet-option">
              <button
                type="button"
                class="cabinet-option-main"
                disabled={!summary.canIssue || !o.available}
                onClick={() => onPick({ provider: VPN_PROVIDER[kind], title: summary.title, option: o })}
              >
                <span class="list-row-main">
                  <span class="row-title">
                    <Quoted text={o.label} />
                  </span>
                  {o.note && <span class="list-row-sub">{o.note}</span>}
                </span>
                {/* Стрелка -- только у того, что можно выбрать: погашенная
                    страна не зовёт нажать. Рядом с «Отозвать» стрелка встала
                    бы посреди строки -- там строку выделяет сама кнопка. */}
                {summary.canIssue && o.available && !(revocable && o.issued) && (
                  <svg class="list-row-chevron" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M 6 3 L 11 8 L 6 13" />
                  </svg>
                )}
              </button>
              {revocable && o.issued && (
                <button type="button" class="btn btn-ghost btn-row cabinet-danger" onClick={() => revoke(o)}>
                  Отозвать
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}
