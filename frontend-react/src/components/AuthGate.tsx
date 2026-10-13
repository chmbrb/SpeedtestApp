import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { auth, loginErrorMessage } from '../shared/state/auth'
import { useAuth } from '../state/useAuth'
import { Mark } from './Shell'

export function LoginScreen() {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !password) return
    setBusy(true)
    setError('')
    try {
      await auth.signIn(password) // on success this screen goes away
    } catch (err) {
      setError(loginErrorMessage(err))
      setPassword('')
      setBusy(false)
    }
  }

  return (
    <main className="auth">
      <form className="auth-card" onSubmit={submit}>
        <div className="auth-brand">
          <Mark />
          <span>Speedtest</span>
        </div>
        <h1>Sign in</h1>
        {/* Password managers want to see a user name; there is only one shared password, so it is a fixed, hidden one. */}
        <input className="sr-only" type="text" name="username" autoComplete="username" defaultValue="speedtest" readOnly tabIndex={-1} aria-hidden="true" />
        <label className="field">
          <span>Password</span>
          <input type="password" name="password" autoComplete="current-password" autoFocus required value={password}
            onChange={(e) => setPassword(e.target.value)} data-testid="login-password" />
        </label>
        <p className="auth-error" role="alert" data-testid="login-error">{error}</p>
        <button type="submit" className="btn primary" disabled={busy || !password} data-testid="login-submit">
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}

/** Shows the dashboard, or the login when this server asks for a password and the browser has none. */
export function AuthGate({ children }: { children: ReactNode }) {
  const { gate } = useAuth()
  const queryClient = useQueryClient()

  useEffect(() => {
    void auth.check()
  }, [])
  // Whatever the dashboard had loaded must not still be in memory once the login is up.
  useEffect(() => {
    if (gate === 'login') queryClient.clear()
  }, [gate, queryClient])

  if (gate === 'checking') return <div className="auth-wait" role="status">Loading…</div>
  if (gate === 'login') return <LoginScreen />
  return <>{children}</>
}
