// Порядок служебных проверок на экране.
//
// Раньше он жил внутри прибора: лампы на корпусе и список «прочих проверок»
// были одним и тем же набором в одном порядке, и выводить его дважды значило
// бы дать им разойтись. Прибор удалён, а причина держать порядок в одном
// месте осталась -- список проверок читают и на главном экране, и на вкладке
// проверок.
const CHECK_ORDER = ['dns', 'external_reach', 'hydraroute', 'awg_manager', 'tunnels']

// Строки `tunnel_*` отфильтрованы намеренно: это VPN-ТУННЕЛИ, и у них своё место на
// экране. Бэкенд кладёт их в `checks` наравне со службами, поэтому без фильтра
// каждый VPN-туннель отрисовался бы дважды -- разом и VPN-туннелем, и «службой».
export function orderChecks(checks) {
  return (checks ?? [])
    .filter((c) => c?.check_name && !c.check_name.startsWith('tunnel_'))
    .slice()
    .sort((a, b) => {
      const ia = CHECK_ORDER.indexOf(a.check_name)
      const ib = CHECK_ORDER.indexOf(b.check_name)
      if (ia !== ib) return (ia < 0 ? Infinity : ia) - (ib < 0 ? Infinity : ib)
      return a.check_name.localeCompare(b.check_name)
    })
}
