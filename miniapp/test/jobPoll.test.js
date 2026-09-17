import { describe, it, expect } from 'vitest'
import { JOB_POLL_MS, JOB_POLL_FAILURE_LIMIT, jobPollStart, jobPollStep, jobPollDone, jobReconnecting, pollJob } from '../src/jobPoll.js'

const running = { id: 'j1', state: 'running', steps: [] }
const success = { id: 'j1', state: 'success', steps: [] }

describe('jobPollStep', () => {
  it('ответ -- живое задание, счёт ошибок сброшен', () => {
    let s = jobPollStep(jobPollStart(), { type: 'error', status: 0 })
    expect(s.failures).toBe(1)
    expect(jobReconnecting(s)).toBe(true)
    s = jobPollStep(s, { type: 'job', job: running })
    expect(s).toEqual({ phase: 'live', job: running, failures: 0 })
    expect(jobReconnecting(s)).toBe(false)
  })

  it('404 -- задание истекло сразу, опрос окончен', () => {
    const s = jobPollStep({ phase: 'live', job: running, failures: 3 }, { type: 'error', status: 404 })
    expect(s.phase).toBe('expired')
    expect(jobPollDone(s)).toBe(true)
    expect(jobReconnecting(s)).toBe(false)
  })

  it('40 сетевых ошибок подряд -- связь потеряна; последнее задание остаётся', () => {
    let s = { phase: 'live', job: running, failures: 0 }
    for (let i = 0; i < JOB_POLL_FAILURE_LIMIT - 1; i++) s = jobPollStep(s, { type: 'error', status: 502 })
    expect(s.phase).toBe('live')
    expect(jobReconnecting(s)).toBe(true)
    s = jobPollStep(s, { type: 'error', status: 0 })
    expect(s.phase).toBe('lost')
    expect(s.job).toBe(running)
    expect(jobPollDone(s)).toBe(true)
  })

  it('завершённое задание -- опрос окончен; истёкшее не оживает', () => {
    expect(jobPollDone({ phase: 'live', job: success, failures: 0 })).toBe(true)
    expect(jobPollDone({ phase: 'live', job: running, failures: 0 })).toBe(false)
    const expired = { phase: 'expired', job: null, failures: 0 }
    expect(jobPollStep(expired, { type: 'job', job: running })).toBe(expired)
  })

  it('константы спеки', () => {
    expect(JOB_POLL_MS).toBe(1500)
    expect(JOB_POLL_FAILURE_LIMIT).toBe(40)
  })
})

describe('pollJob', () => {
  it('спрашивает до завершения с паузой 1,5 с', async () => {
    const replies = [running, new Error('net'), running, success]
    const asked = []
    const sleeps = []
    const states = []
    const final = await pollJob({
      jobId: 'j1',
      fetchJob: async (id) => {
        asked.push(id)
        const r = replies.shift()
        if (r instanceof Error) throw r
        return r
      },
      sleep: async (ms) => { sleeps.push(ms) },
      onState: (s) => states.push(s.phase),
      signal: { cancelled: false },
    })
    expect(asked).toEqual(['j1', 'j1', 'j1', 'j1'])
    expect(sleeps).toEqual([1500, 1500, 1500])
    expect(final.phase).toBe('live')
    expect(final.job).toBe(success)
    expect(states[0]).toBe('loading')
  })

  it('отмена -- больше ни одного запроса', async () => {
    const signal = { cancelled: false }
    let asked = 0
    await pollJob({
      jobId: 'j1',
      fetchJob: async () => {
        asked++
        return running
      },
      sleep: async () => { signal.cancelled = true },
      onState: () => {},
      signal,
    })
    expect(asked).toBe(1)
  })

  it('ответ, пришедший после отмены, не применяется', async () => {
    const signal = { cancelled: false }
    const states = []
    await pollJob({
      jobId: 'j1',
      fetchJob: async () => {
        signal.cancelled = true
        return success
      },
      sleep: async () => {},
      onState: (s) => states.push(s.phase),
      signal,
    })
    expect(states).toEqual(['loading'])
  })
})
