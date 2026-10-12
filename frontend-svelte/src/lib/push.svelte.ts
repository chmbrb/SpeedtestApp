import { api, expectOk } from '../shared/api/client'
import { explain } from '../shared/lib/explain'
import { currentEnv, pushSupport, sameKey, urlBase64ToBytes, type PushSupport } from '../shared/lib/platform'
import { pushConfig } from './data.svelte'

export type PushState =
  | { status: 'loading' }
  | { status: 'server-off' }
  | { status: 'unreachable' }
  | { status: 'blocked'; why: Exclude<PushSupport['kind'], 'ok'> | 'no-worker' }
  | { status: 'denied' }
  | { status: 'off' }
  | { status: 'on' }

/** How long the background worker gets to start before the person is told it is not going to. */
export const WORKER_WAIT_MS = 8000

async function register(sub: PushSubscription) {
  const body = sub.toJSON() as { endpoint: string; expirationTime?: number | null; keys: { p256dh: string; auth: string } }
  await expectOk(api.POST('/api/v1/push/subscriptions', { body }))
}

/** Notifications for this browser: what is possible here, whether they are on, and how to change that. Same behaviour as
 *  the React hook (frontend-react/src/state/usePush.ts). Create it while a component is being set up. */
export class Push {
  busy = $state(false)
  error = $state<string | null>(null)
  testSent = $state(false)

  #config = pushConfig()
  #support = pushSupport(currentEnv())
  #subscribed = $state<boolean | null>(null)
  #denied = $state('Notification' in window && Notification.permission === 'denied')
  // The background worker never starts when the page was opened by clicking past a certificate warning: browsers refuse to
  // register one on a connection they do not trust. Without this the card would say "Checking…" for ever.
  #workerSlow = $state(false)

  constructor() {
    // On load: find out whether this browser is already subscribed, and keep the server in step with it. A subscription made
    // for an older server key can never work, so it is replaced quietly (permission was already given).
    $effect(() => {
      const publicKey = this.#config.data?.enabled ? this.#config.data.publicKey : undefined
      if (this.#support.kind !== 'ok' || !publicKey) return
      let cancelled = false
      const timer = setTimeout(() => { this.#workerSlow = true }, WORKER_WAIT_MS)
      ;(async () => {
        try {
          const reg = await navigator.serviceWorker.ready
          clearTimeout(timer)
          if (!cancelled) this.#workerSlow = false
          let sub = await reg.pushManager.getSubscription()
          if (sub && Notification.permission === 'granted') {
            const key = urlBase64ToBytes(publicKey)
            if (!sameKey(sub.options.applicationServerKey, key)) {
              await sub.unsubscribe()
              sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
            }
            await register(sub)
          }
          if (!cancelled) this.#subscribed = !!sub && Notification.permission === 'granted'
        } catch {
          if (!cancelled) this.#subscribed = false
        }
      })()
      return () => { cancelled = true; clearTimeout(timer) }
    })
  }

  state: PushState = $derived.by(() => {
    const c = this.#config
    if (c.loading) return { status: 'loading' }
    if (c.data && !c.data.enabled) return { status: 'server-off' }
    if (this.#support.kind !== 'ok') return { status: 'blocked', why: this.#support.kind }
    if (!c.data) return { status: 'unreachable' }
    if (this.#denied) return { status: 'denied' }
    if (this.#subscribed === null) return this.#workerSlow ? { status: 'blocked', why: 'no-worker' } : { status: 'loading' }
    return { status: this.#subscribed ? 'on' : 'off' }
  })

  async #run(fn: () => Promise<void>) {
    this.busy = true
    this.error = null
    this.testSent = false
    try {
      await fn()
    } catch (e) {
      this.error = explain(e)
    } finally {
      this.busy = false
    }
  }

  enable = () => this.#run(async () => {
    const publicKey = this.#config.data?.enabled ? this.#config.data.publicKey : undefined
    if (!publicKey) return
    const permission = await Notification.requestPermission()
    if (permission === 'denied') { this.#denied = true; return }
    if (permission !== 'granted') return // the dialog was closed without an answer
    const reg = await navigator.serviceWorker.ready
    const sub = (await reg.pushManager.getSubscription()) ?? await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToBytes(publicKey),
    })
    await register(sub)
    this.#subscribed = true
  })

  disable = () => this.#run(async () => {
    const reg = await navigator.serviceWorker.ready
    const sub = await reg.pushManager.getSubscription()
    if (sub) {
      // Tell the server first: if that fails the person can retry, and nothing is left half-removed.
      await expectOk(api.DELETE('/api/v1/push/subscriptions', { body: { endpoint: sub.endpoint } }))
      await sub.unsubscribe()
    }
    this.#subscribed = false
  })

  sendTest = () => this.#run(async () => {
    const reg = await navigator.serviceWorker.ready
    const sub = await reg.pushManager.getSubscription()
    if (!sub) { this.#subscribed = false; return }
    await expectOk(api.POST('/api/v1/push/test', { body: { endpoint: sub.endpoint } }))
    this.testSent = true
  })
}
