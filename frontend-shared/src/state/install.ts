import { currentEnv } from '../lib/platform'

// Chromium-based browsers offer the install prompt once, early, as an event. It has to be caught before the app renders,
// so the app's entry point calls initInstall() first and its view reads what was caught. No UI framework in here.

interface InstallEvent extends Event {
  prompt(): Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

const DISMISS_KEY = 'install-dismissed-at'
const DISMISS_DAYS = 30

let deferred: InstallEvent | null = null
let installed = false
const listeners = new Set<() => void>()
let snapshot = compute()

function dismissedRecently(): boolean {
  try {
    const at = Number(localStorage.getItem(DISMISS_KEY))
    return at > 0 && Date.now() - at < DISMISS_DAYS * 86_400_000
  } catch {
    return false
  }
}

export interface InstallState {
  /** The browser can show its own install dialog. */
  canPrompt: boolean
  /** iPhone or iPad: no dialog exists, so the person is told where the buttons are. */
  showIosHint: boolean
  /** Already running as an installed app. */
  installed: boolean
  /** The person said "not now" recently. */
  dismissed: boolean
}

function compute(): InstallState {
  const env = typeof window === 'undefined' ? null : currentEnv()
  const isInstalled = installed || !!env?.standalone
  return {
    canPrompt: deferred !== null && !isInstalled,
    showIosHint: !!env?.ios && !isInstalled,
    installed: isInstalled,
    dismissed: dismissedRecently(),
  }
}

function emit() {
  snapshot = compute()
  listeners.forEach((l) => l())
}

export function initInstall(): void {
  window.addEventListener('beforeinstallprompt', (e) => {
    e.preventDefault() // keep the browser's own mini-bar away; the app offers the button where it fits
    deferred = e as InstallEvent
    emit()
  })
  window.addEventListener('appinstalled', () => {
    installed = true
    deferred = null
    emit()
  })
  window.matchMedia('(display-mode: standalone)').addEventListener('change', emit)
}

export function dismissInstall(): void {
  try {
    localStorage.setItem(DISMISS_KEY, String(Date.now()))
  } catch {
    // Storage can be blocked; the banner then returns on the next visit, which is acceptable.
  }
  emit()
}

export async function promptInstall(): Promise<'accepted' | 'dismissed' | 'unavailable'> {
  if (!deferred) return 'unavailable'
  const ev = deferred
  deferred = null // an install prompt can be used once
  emit()
  await ev.prompt()
  const { outcome } = await ev.userChoice
  return outcome
}

export function getInstallState(): InstallState {
  return snapshot
}

/** For `useSyncExternalStore` (React) or a Svelte readable store: calls `cb` after every change, returns the unsubscribe. */
export function subscribeInstall(cb: () => void): () => void {
  listeners.add(cb)
  return () => { listeners.delete(cb) }
}
