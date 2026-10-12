import { MINUTE } from '../shared/lib/time'

const floor = (t: number) => Math.floor(t / MINUTE) * MINUTE

/** The current time, floored to the minute and refreshed every minute, so query keys stay stable between ticks. */
class Clock {
  now = $state(floor(Date.now()))
  constructor() {
    setInterval(() => { this.now = floor(Date.now()) }, MINUTE)
  }
}

export const clock = new Clock()
