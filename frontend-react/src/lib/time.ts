import { useEffect, useState } from 'react'

export const MINUTE = 60_000
export const HOUR = 60 * MINUTE
export const DAY = 24 * HOUR

/** The viewer's IANA zone; chart buckets are aligned to it so a "day" matches the viewer's calendar day. */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

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
