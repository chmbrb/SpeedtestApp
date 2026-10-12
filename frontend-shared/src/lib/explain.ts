import { ApiError } from '../api/client'

/** Words for what went wrong, for a person rather than a programmer. */
export function explain(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 409) return 'Too many devices already have notifications on. Turn them off on one of those first.'
    if (e.status === 429) return 'Too many tries in a row. Wait a minute and try again.'
    if (e.status === 404) return 'The server does not know this device yet. Turn notifications off and on again.'
    if (e.status === 502) return `The browser’s notification service did not accept the message. ${e.message}`
    return e.message
  }
  const name = e instanceof Error ? e.name : ''
  if (name === 'NotAllowedError') return 'Notifications are blocked for this site. Allow them in your browser’s site settings, then try again.'
  if (name === 'AbortError' || name === 'InvalidStateError' || name === 'NetworkError') {
    return 'Your browser could not reach its notification service. Check your internet connection and try again.'
  }
  return 'Something went wrong while turning notifications on. Try again.'
}
