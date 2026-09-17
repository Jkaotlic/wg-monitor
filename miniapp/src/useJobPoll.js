import { useEffect, useState } from 'preact/hooks'
import { fetchJob } from './api.js'
import { jobPollStart, pollJob } from './jobPoll.js'

const realSleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// Опрос задания, пока экран на месте. Уход с экрана останавливает опрос, но
// не задание: оно живёт на сервере. Новый jobId -- новый опрос с нуля.
export function useJobPoll(jobId, { sleep = realSleep } = {}) {
  const [state, setState] = useState(jobPollStart)
  useEffect(() => {
    const signal = { cancelled: false }
    setState(jobPollStart())
    pollJob({
      jobId,
      fetchJob,
      sleep,
      signal,
      onState: (s) => {
        if (!signal.cancelled) setState(s)
      },
    })
    return () => {
      signal.cancelled = true
    }
  }, [jobId])
  return state
}
