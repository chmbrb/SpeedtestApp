import { api, ApiError, expectOk, unwrap } from '../api/client'

/**
 * The optional dashboard login, for both apps. A server with no password set never asks for one: the gate is `open` straight away.
 *
 * - `checking`: the first answer from the server is not in yet.
 * - `open`: show the dashboard (no login on this server, this browser is signed in, or the server cannot be reached right now and
 *   the data this device saved is all there is to show).
 * - `login`: the server asks for the password.
 */
export type Gate = 'checking' | 'open' | 'login'

export interface AuthSnapshot {
  gate: Gate
  /** The server has a login, so there is something to sign out of. */
  required: boolean
}

export interface AuthDeps {
  session(): Promise<{ required: boolean; authenticated: boolean }>
  login(password: string): Promise<void>
  logout(): Promise<void>
  /** Throw away what this device saved of the signed-in session, so nobody sees it after the screen locks. */
  forget(): Promise<void>
}

export interface Auth {
  getSnapshot(): AuthSnapshot
  subscribe(listener: () => void): () => void
  /** Ask the server where things stand. Safe to call more than once. */
  check(): Promise<void>
  /** Rejects with the server's refusal (an ApiError) when the password is wrong or too many were tried. */
  signIn(password: string): Promise<void>
  /** Rejects when the server could not be told, because then the cookie is still there and nothing was signed out. */
  signOut(): Promise<void>
  /** An API call was refused with 401: the session ended (it ran out, or the password was changed). */
  expired(): void
}

export function createAuth(deps: AuthDeps): Auth {
  let snap: AuthSnapshot = { gate: 'checking', required: false }
  const listeners = new Set<() => void>()
  const set = (next: AuthSnapshot) => {
    if (next.gate === snap.gate && next.required === snap.required) return
    snap = next
    listeners.forEach((l) => l())
  }
  let checking: Promise<void> | null = null

  const lock = async () => {
    await deps.forget()
    set({ gate: 'login', required: true })
  }

  return {
    getSnapshot: () => snap,
    subscribe(listener) {
      listeners.add(listener)
      return () => void listeners.delete(listener)
    },
    check() {
      checking ??= (async () => {
        try {
          const s = await deps.session()
          if (s.required && !s.authenticated) await lock()
          else set({ gate: 'open', required: s.required })
        } catch {
          // The server cannot be reached (offline, or restarting): show what this device has saved. If a request gets through
          // later and is refused, `expired` brings the login up.
          set({ gate: 'open', required: snap.required })
        } finally {
          checking = null
        }
      })()
      return checking
    },
    async signIn(password) {
      await deps.login(password)
      set({ gate: 'open', required: true })
    },
    async signOut() {
      await deps.logout()
      await lock()
    },
    expired() {
      if (snap.gate === 'open') void lock()
    },
  }
}

/** What to tell a person whose attempt was refused. */
export function loginErrorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 401) return 'That password is not right.'
    if (e.status === 429) return 'Too many attempts. Wait a minute and try again.'
    return e.message
  }
  return 'The server could not be reached.'
}

/** Drops the copy of API answers the service worker keeps for offline use (the cache name is the one in sw.ts). */
export async function clearApiCache(): Promise<void> {
  try {
    await globalThis.caches?.delete('api-v1')
  } catch {
    // no Cache Storage here (private window, old browser): nothing was kept
  }
}

export const auth: Auth = createAuth({
  session: () => unwrap(api.GET('/api/v1/auth/session')),
  login: (password) => expectOk(api.POST('/api/v1/auth/login', { body: { password } })),
  logout: () => expectOk(api.POST('/api/v1/auth/logout')),
  forget: clearApiCache,
})

// Any call the server refuses with 401 means the session is over. Not the login itself (a wrong password is also 401), and not
// calls that carry their own bearer token (a browser test submitting its result): those 401s are about the token.
api.use({
  onResponse({ request, response }) {
    if (response.status === 401 && !request.headers.has('authorization') && !new URL(request.url).pathname.startsWith('/api/v1/auth/')) {
      auth.expired()
    }
  },
})
