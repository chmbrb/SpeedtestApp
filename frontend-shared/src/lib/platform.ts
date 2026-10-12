// What this browser can do about installing the app and receiving notifications. Pure functions, so they can be tested.

export interface Env {
  secureContext: boolean
  hasServiceWorker: boolean
  hasPushManager: boolean
  hasNotification: boolean
  ios: boolean
  standalone: boolean
}

export type PushSupport =
  | { kind: 'ok' }
  | { kind: 'insecure' } // not https (and not localhost): browsers switch notifications off
  | { kind: 'install-first' } // iPhone and iPad only allow notifications for an app added to the Home Screen
  | { kind: 'unsupported' }

export function pushSupport(e: Env): PushSupport {
  if (!e.secureContext) return { kind: 'insecure' }
  if (e.ios && !e.standalone) return { kind: 'install-first' }
  if (!e.hasServiceWorker || !e.hasPushManager || !e.hasNotification) return { kind: 'unsupported' }
  return { kind: 'ok' }
}

/** iPads report themselves as Macs, so a Mac with a touch screen is an iPad. */
export function isIOS(userAgent: string, platform: string, touchPoints: number): boolean {
  return /iPad|iPhone|iPod/.test(userAgent) || (platform === 'MacIntel' && touchPoints > 1)
}

export function currentEnv(): Env {
  const nav = navigator as Navigator & { standalone?: boolean }
  return {
    secureContext: window.isSecureContext,
    hasServiceWorker: 'serviceWorker' in navigator,
    hasPushManager: 'PushManager' in window,
    hasNotification: 'Notification' in window,
    ios: isIOS(navigator.userAgent, navigator.platform, navigator.maxTouchPoints),
    standalone: window.matchMedia('(display-mode: standalone)').matches || nav.standalone === true,
  }
}

/** The VAPID public key arrives base64url encoded; the browser wants bytes. */
export function urlBase64ToBytes(b64: string): Uint8Array<ArrayBuffer> {
  const padded = b64.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (b64.length % 4)) % 4)
  const raw = atob(padded)
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

/** True when a subscription was made with this server key (a new server key means the old subscription can never work). */
export function sameKey(existing: ArrayBuffer | null | undefined, wanted: Uint8Array): boolean {
  if (!existing) return false
  const a = new Uint8Array(existing)
  return a.length === wanted.length && a.every((v, i) => v === wanted[i])
}
