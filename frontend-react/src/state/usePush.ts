import { useCallback, useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, expectOk, unwrap } from '../shared/api/client'
import { explain } from '../shared/lib/explain'
import { currentEnv, pushSupport, sameKey, urlBase64ToBytes, type PushSupport } from '../shared/lib/platform'

export type PushState =
  | { status: 'loading' }
  | { status: 'server-off' }
  | { status: 'unreachable' }
  | { status: 'blocked'; why: Exclude<PushSupport['kind'], 'ok'> | 'no-worker' }
  | { status: 'denied' }
  | { status: 'off' }
  | { status: 'on' }

export interface PushControls {
  state: PushState
  busy: boolean
  error: string | null
  testSent: boolean
  enable: () => Promise<void>
  disable: () => Promise<void>
  sendTest: () => Promise<void>
}

/** How long the background worker gets to start before the person is told it is not going to. */
export const WORKER_WAIT_MS = 8000

export function usePushConfig() {
  return useQuery({
    queryKey: ['push-config'],
    queryFn: () => unwrap(api.GET('/api/v1/push/config')),
    staleTime: 5 * 60_000,
  })
}

async function register(sub: PushSubscription) {
  const body = sub.toJSON() as { endpoint: string; expirationTime?: number | null; keys: { p256dh: string; auth: string } }
  await expectOk(api.POST('/api/v1/push/subscriptions', { body }))
}

/** Notifications for this browser: what is possible here, whether they are on, and how to change that. */
export function usePush(): PushControls {
  const config = usePushConfig()
  const [env] = useState(currentEnv)
  const support = pushSupport(env)
  const [subscribed, setSubscribed] = useState<boolean | null>(null)
  const [denied, setDenied] = useState(() => 'Notification' in window && Notification.permission === 'denied')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [testSent, setTestSent] = useState(false)
  // The background worker never starts when the page was opened by clicking past a certificate warning: browsers refuse to
  // register one on a connection they do not trust. Without this the card would say "Checking…" for ever.
  const [workerSlow, setWorkerSlow] = useState(false)

  const publicKey = config.data?.enabled ? config.data.publicKey : undefined
  const canCheck = support.kind === 'ok' && !!publicKey

  // On load: find out whether this browser is already subscribed, and keep the server in step with it. A subscription made
  // for an older server key can never work, so it is replaced quietly (permission was already given).
  useEffect(() => {
    if (!canCheck || !publicKey) return
    let cancelled = false
    const timer = setTimeout(() => setWorkerSlow(true), WORKER_WAIT_MS)
    ;(async () => {
      try {
        const reg = await navigator.serviceWorker.ready
        clearTimeout(timer)
        if (!cancelled) setWorkerSlow(false)
        let sub = await reg.pushManager.getSubscription()
        if (sub && Notification.permission === 'granted') {
          const key = urlBase64ToBytes(publicKey)
          if (!sameKey(sub.options.applicationServerKey, key)) {
            await sub.unsubscribe()
            sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
          }
          await register(sub)
        }
        if (!cancelled) setSubscribed(!!sub && Notification.permission === 'granted')
      } catch {
        if (!cancelled) setSubscribed(false)
      }
    })()
    return () => { cancelled = true; clearTimeout(timer) }
  }, [canCheck, publicKey])

  const run = useCallback(async (fn: () => Promise<void>) => {
    setBusy(true)
    setError(null)
    setTestSent(false)
    try {
      await fn()
    } catch (e) {
      setError(explain(e))
    } finally {
      setBusy(false)
    }
  }, [])

  const enable = useCallback(() => run(async () => {
    if (!publicKey) return
    const permission = await Notification.requestPermission()
    if (permission === 'denied') { setDenied(true); return }
    if (permission !== 'granted') return // the dialog was closed without an answer
    const reg = await navigator.serviceWorker.ready
    const sub = (await reg.pushManager.getSubscription()) ?? await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToBytes(publicKey),
    })
    await register(sub)
    setSubscribed(true)
  }), [publicKey, run])

  const disable = useCallback(() => run(async () => {
    const reg = await navigator.serviceWorker.ready
    const sub = await reg.pushManager.getSubscription()
    if (sub) {
      // Tell the server first: if that fails the person can retry, and nothing is left half-removed.
      await expectOk(api.DELETE('/api/v1/push/subscriptions', { body: { endpoint: sub.endpoint } }))
      await sub.unsubscribe()
    }
    setSubscribed(false)
  }), [run])

  const sendTest = useCallback(() => run(async () => {
    const reg = await navigator.serviceWorker.ready
    const sub = await reg.pushManager.getSubscription()
    if (!sub) { setSubscribed(false); return }
    await expectOk(api.POST('/api/v1/push/test', { body: { endpoint: sub.endpoint } }))
    setTestSent(true)
  }), [run])

  let state: PushState
  if (config.isLoading) state = { status: 'loading' }
  else if (config.data && !config.data.enabled) state = { status: 'server-off' }
  else if (support.kind !== 'ok') state = { status: 'blocked', why: support.kind }
  else if (!config.data) state = { status: 'unreachable' }
  else if (denied) state = { status: 'denied' }
  else if (subscribed === null) state = workerSlow ? { status: 'blocked', why: 'no-worker' } : { status: 'loading' }
  else state = { status: subscribed ? 'on' : 'off' }

  return { state, busy, error, testSent, enable, disable, sendTest }
}
