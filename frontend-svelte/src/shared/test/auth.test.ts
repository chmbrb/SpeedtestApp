import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { clearApiCache, createAuth, loginErrorMessage, type AuthDeps } from '../state/auth'

function rig(session: AuthDeps['session']) {
  const deps = {
    session: vi.fn(session),
    login: vi.fn(async () => {}),
    logout: vi.fn(async () => {}),
    forget: vi.fn(async () => {}),
  }
  return { deps, auth: createAuth(deps) }
}

afterEach(() => vi.unstubAllGlobals())

describe('the login gate', () => {
  it('starts by checking', () => {
    expect(rig(async () => ({ required: false, authenticated: true })).auth.getSnapshot()).toEqual({ gate: 'checking', required: false })
  })

  it('opens at once on a server without a login', async () => {
    const { auth, deps } = rig(async () => ({ required: false, authenticated: true }))
    await auth.check()
    expect(auth.getSnapshot()).toEqual({ gate: 'open', required: false })
    expect(deps.forget).not.toHaveBeenCalled()
  })

  it('opens when this browser is already signed in', async () => {
    const { auth } = rig(async () => ({ required: true, authenticated: true }))
    await auth.check()
    expect(auth.getSnapshot()).toEqual({ gate: 'open', required: true })
  })

  it('asks for the password when it is required and this browser has none, and drops what the device saved', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: false }))
    await auth.check()
    expect(auth.getSnapshot()).toEqual({ gate: 'login', required: true })
    expect(deps.forget).toHaveBeenCalledOnce()
  })

  it('shows what the device has saved when the server cannot be reached, and does not throw it away', async () => {
    const { auth, deps } = rig(async () => {
      throw new TypeError('Failed to fetch')
    })
    await auth.check()
    expect(auth.getSnapshot().gate).toBe('open')
    expect(deps.forget).not.toHaveBeenCalled()
  })

  it('keeps knowing a login exists when a later check cannot reach the server', async () => {
    let up = true
    const { auth } = rig(async () => {
      if (!up) throw new TypeError('offline')
      return { required: true, authenticated: true }
    })
    await auth.check()
    up = false
    await auth.check()
    expect(auth.getSnapshot()).toEqual({ gate: 'open', required: true })
  })

  it('asks the server once for checks made together', async () => {
    const { auth, deps } = rig(async () => ({ required: false, authenticated: true }))
    await Promise.all([auth.check(), auth.check(), auth.check()])
    expect(deps.session).toHaveBeenCalledOnce()
    await auth.check()
    expect(deps.session).toHaveBeenCalledTimes(2)
  })

  it('opens after the right password', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: false }))
    await auth.check()
    await auth.signIn('open sesame')
    expect(deps.login).toHaveBeenCalledWith('open sesame')
    expect(auth.getSnapshot()).toEqual({ gate: 'open', required: true })
  })

  it('stays on the login, and passes the refusal on, after a wrong password', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: false }))
    await auth.check()
    deps.login.mockRejectedValueOnce(new ApiError(401, 'incorrect password'))
    await expect(auth.signIn('nope')).rejects.toMatchObject({ status: 401 })
    expect(auth.getSnapshot().gate).toBe('login')
  })

  it('goes back to the login on sign out, and drops what the device saved', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: true }))
    await auth.check()
    await auth.signOut()
    expect(deps.logout).toHaveBeenCalledOnce()
    expect(deps.forget).toHaveBeenCalledOnce()
    expect(auth.getSnapshot()).toEqual({ gate: 'login', required: true })
  })

  it('does not pretend to sign out when the server could not be told (the cookie is still there)', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: true }))
    await auth.check()
    deps.logout.mockRejectedValueOnce(new TypeError('offline'))
    await expect(auth.signOut()).rejects.toThrow()
    expect(auth.getSnapshot().gate).toBe('open')
    expect(deps.forget).not.toHaveBeenCalled()
  })

  it('shows the login when a call is refused because the session ended', async () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: true }))
    await auth.check()
    auth.expired()
    await vi.waitFor(() => expect(auth.getSnapshot().gate).toBe('login'))
    expect(deps.forget).toHaveBeenCalledOnce()
    auth.expired() // many calls fail together: only the first one matters
    expect(deps.forget).toHaveBeenCalledOnce()
  })

  it('ignores a refusal before the first answer is in', () => {
    const { auth, deps } = rig(async () => ({ required: true, authenticated: true }))
    auth.expired()
    expect(auth.getSnapshot().gate).toBe('checking')
    expect(deps.forget).not.toHaveBeenCalled()
  })

  it('tells listeners about each change once, and not after they unsubscribe', async () => {
    const { auth } = rig(async () => ({ required: true, authenticated: false }))
    const seen = vi.fn()
    const off = auth.subscribe(seen)
    await auth.check()
    expect(seen).toHaveBeenCalledOnce()
    await auth.check() // nothing changed
    expect(seen).toHaveBeenCalledOnce()
    off()
    await auth.signIn('x')
    expect(seen).toHaveBeenCalledOnce()
  })
})

describe('loginErrorMessage', () => {
  it('says what to do', () => {
    expect(loginErrorMessage(new ApiError(401, 'incorrect password'))).toBe('That password is not right.')
    expect(loginErrorMessage(new ApiError(429, 'too many login attempts; wait a moment'))).toMatch(/Too many attempts/)
    expect(loginErrorMessage(new ApiError(500, 'internal error'))).toBe('internal error')
    expect(loginErrorMessage(new TypeError('Failed to fetch'))).toBe('The server could not be reached.')
  })
})

describe('clearApiCache', () => {
  it('deletes the cache the service worker keeps API answers in', async () => {
    const del = vi.fn(async () => true)
    vi.stubGlobal('caches', { delete: del })
    await clearApiCache()
    expect(del).toHaveBeenCalledWith('api-v1')
  })

  it('is fine without Cache Storage, or when it throws', async () => {
    vi.stubGlobal('caches', undefined)
    await expect(clearApiCache()).resolves.toBeUndefined()
    vi.stubGlobal('caches', { delete: async () => { throw new Error('denied') } })
    await expect(clearApiCache()).resolves.toBeUndefined()
  })
})
