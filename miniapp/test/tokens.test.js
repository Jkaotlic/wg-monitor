import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, it, expect } from 'vitest'

const SRC = fileURLToPath(new URL('../src', import.meta.url))

// theme.js -- сама палитра. style.css -- её CSS-зеркало. RouterDevice.jsx --
// рисунок железа: корпус, антенны и лампы имеют собственную материальную
// палитру (металл, вентиляция, свечение), которая не является частью
// интерфейсной темы и в токены не выносится.
const ALLOWED = new Set(['theme.js', 'style.css', 'RouterDevice.jsx'])

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

describe('дисциплина токенов', () => {
  const files = walk(SRC).filter((p) => !ALLOWED.has(p.split('/').pop()))
  for (const file of files) {
    it(`${file.slice(SRC.length + 1)} не содержит hex-цветов`, () => {
      const hits = readFileSync(file, 'utf8').match(/#[0-9a-fA-F]{3,8}\b/g) ?? []
      expect(hits).toEqual([])
    })
  }
})
