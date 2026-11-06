import { expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

it('production manifest keeps core tabs eager and heavy admin screens lazy', () => {
  const manifest = JSON.parse(readFileSync(new URL('../../internal/backend/miniapp_static/manifest.json', import.meta.url), 'utf8'))
  const entries = Object.values(manifest)
  const main = entries.find((entry) => entry.isEntry)
  expect(main?.file).toBeTruthy()
  for (const screen of ['ParkTab.jsx', 'ManageTab.jsx', 'ProvisionWizard.jsx', 'CabinetScreen.jsx']) {
    const chunk = Object.entries(manifest).find(([key]) => key.endsWith(screen))?.[1]
    expect(chunk?.isDynamicEntry, `${screen} must be a lazy chunk`).toBe(true)
  }
  expect(main.file).not.toMatch(/ParkTab|ManageTab|ProvisionWizard|CabinetScreen/)
})
