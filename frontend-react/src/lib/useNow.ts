import { useEffect, useState } from 'react'
import { MINUTE } from '../shared/lib/time'

/** Current time, floored to the minute and refreshed every `everyMs`, so query keys stay stable between ticks. */
export function useNow(everyMs = MINUTE): number {
  const floor = (t: number) => Math.floor(t / MINUTE) * MINUTE
  const [now, setNow] = useState(() => floor(Date.now()))
  useEffect(() => {
    const id = setInterval(() => setNow(floor(Date.now())), everyMs)
    return () => clearInterval(id)
  }, [everyMs])
  return now
}
