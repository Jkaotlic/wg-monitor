import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  DNS_REFERENCE_FOREIGN,
  DNS_REFERENCE_RU_ZONES,
  DNS_REFERENCE_YANDEX,
  dnsReferenceCommands,
  dnsResetScreenTexts,
} from '../src/dnsReset.js'

const GO = readFileSync(fileURLToPath(new URL('../../internal/agent/dnsref/dnsref.go', import.meta.url)), 'utf8')

// Строки из `name = []string{ … }` в dnsref.go.
function goStrings(name) {
  const at = GO.indexOf(`${name} = []string{`)
  expect(at, name).toBeGreaterThan(-1)
  const body = GO.slice(at, GO.indexOf('}', at))
  return [...body.matchAll(/"([^"]*)"/g)].map((m) => m[1])
}

describe('эталон ручного сброса совпадает с агентом', () => {
  it('заграничная часть, русские зоны и хост Яндекса -- как в dnsref.go', () => {
    expect(DNS_REFERENCE_FOREIGN).toEqual(goStrings('referenceForeignDoT'))
    expect(DNS_REFERENCE_RU_ZONES).toEqual(goStrings('ruZones'))
    expect(GO).toContain(`const yandexDoTHost = "${DNS_REFERENCE_YANDEX}"`)
  })

  it('команды: сначала заграничные, затем зона за зоной, в конце сохранение', () => {
    const lines = dnsReferenceCommands().split('\n')
    expect(lines[0]).toBe('ndmc -c "dns-proxy tls upstream 9.9.9.9 sni dns.quad9.net"')
    expect(lines[1]).toBe('ndmc -c "dns-proxy tls upstream 1.1.1.1 sni cloudflare-dns.com"')
    expect(lines[2]).toBe('ndmc -c "dns-proxy tls upstream common.dot.dns.yandex.net domain ru"')
    expect(lines).toHaveLength(DNS_REFERENCE_FOREIGN.length + DNS_REFERENCE_RU_ZONES.length + 1)
    expect(lines.at(-1)).toBe('ndmc -c "system configuration save"')
  })

  it('тексты блока ручного прогона предупреждают о дублях', () => {
    const T = dnsResetScreenTexts()
    expect(T.manualTitle).toBe('Команды для ручного прогона')
    expect(T.manualIntro).toContain('не снимают прежние')
    expect(T.copyButton).toBe('Скопировать команды')
    expect(T.copied).toBe('Команды скопированы.')
    expect(T.copyFailed).toBe('Не удалось скопировать — выделите текст вручную.')
  })
})

describe('предпросмотр при лимите KeenOS', () => {
  it('называет строки, которые не поместятся', async () => {
    const { parsePreview, previewText } = await import('../src/dnsReset.js')
    const out = [
      'Предпросмотр сброса DNS — ничего не изменено', '', 'Уберём (0):', '',
      'Оставим как есть (1):', '  = tls upstream 198.51.100.53 sni resolver.example.com', '',
      'Заменим на эталонные (7):', '  + tls upstream 9.9.9.9 sni dns.quad9.net', '',
      'не поместилось в лимит KeenOS (8 адресов), не ставим (1):', '  · tls upstream common.dot.dns.yandex.net domain xn--p1acf',
    ].join('\n')
    const p = parsePreview(out)
    expect(p.addCount).toBe(7)
    expect(p.skippedCount).toBe(1)
    expect(previewText(p)).toContain('1 строка эталона не поместится')
  })
})
