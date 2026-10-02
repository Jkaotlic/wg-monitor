import { describe, it, expect } from 'vitest'
import { spawn } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { stopChild } from '../layout/proc.mjs'

const alive = (pid) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

describe('скрипт раскладки: остановка песочницы', () => {
  it('stopChild возвращается, когда процесс уже завершился', async () => {
    const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' })
    await new Promise((r) => child.once('spawn', r))
    expect(alive(child.pid)).toBe(true)
    await stopChild(child)
    expect(alive(child.pid)).toBe(false)
    expect(child.exitCode !== null || child.signalCode !== null).toBe(true)
  })
  it('процесс, глухой к SIGTERM, добивается SIGKILL по истечении срока', async () => {
    const child = spawn(process.execPath, ['-e', "process.on('SIGTERM', () => {}); console.log('ready'); setInterval(() => {}, 1000)"], { detached: true, stdio: ['ignore', 'pipe', 'ignore'] })
    await new Promise((r) => child.stdout.once('data', r))
    await stopChild(child, { graceMs: 300 })
    expect(alive(child.pid)).toBe(false)
    expect(child.signalCode).toBe('SIGKILL')
  })
  it('уже мёртвый процесс -- не ошибка', async () => {
    const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' })
    await new Promise((r) => child.once('exit', r))
    await expect(stopChild(child)).resolves.toBeUndefined()
  })
  it('npm-скрипты прогона и самопроверки подключены', () => {
    const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'))
    expect(pkg.scripts.layout).toBe('node layout/run.mjs')
    expect(pkg.scripts['layout:selftest']).toBe('node layout/selftest.mjs')
  })
})
