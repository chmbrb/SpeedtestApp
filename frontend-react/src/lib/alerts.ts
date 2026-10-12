import type { NotificationEvent, NotificationRules } from '../api/client'

const pct = (ratio: number) => `${Math.round(ratio * 100)}%`
const times = (n: number) => (n === 1 ? 'one test' : `${n} tests in a row`)
const hours = (h: number) => (h === 1 ? '1 hour' : `${Number.isInteger(h) ? h : h.toFixed(1)} hours`)

/** The alert rules as sentences a person can read, so the dashboard states exactly what will notify them. */
export function describeRules(r: NotificationRules): string[] {
  return [
    `A device’s download is below ${pct(r.slowRatio)} of its typical speed for ${times(r.slowConsecutive)}. ` +
      `Typical is the median of the last ${r.baselineDays} days. The alert ends when the speed is back above ${pct(r.recoverRatio)}.`,
    `Packet loss is ${r.lossPct}% or more for ${times(r.lossConsecutive)}.`,
    `A device stops running its scheduled tests: nothing for three times its usual interval, and never sooner than ${hours(r.silenceMinHours)}.`,
    r.remindHours > 0
      ? `While a problem lasts you get a reminder every ${hours(r.remindHours)}, for up to a week.`
      : 'Reminders are off: you hear about a problem once, and again when it clears.',
  ]
}

const LABELS: Record<NotificationEvent['event'], string> = {
  alert: 'Alert', recovery: 'Recovered', reminder: 'Reminder', test: 'Test',
}
export const eventLabel = (e: NotificationEvent['event']) => LABELS[e]
