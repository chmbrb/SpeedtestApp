import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../shared/api/client'
import { AuthGate } from './AuthGate'

// A stand-in for the login state, so these tests are about the screens and not about the server.
const fake = vi.hoisted(() => {
  let snap = { gate: 'checking', required: false }
  const listeners = new Set<() => void>()
  return {
    auth: {
      getSnapshot: () => snap,
      subscribe: (l: () => void) => {
        listeners.add(l)
        return () => void listeners.delete(l)
      },
      check: vi.fn(async () => {}),
      signIn: vi.fn(async (_: string) => {}),
      signOut: vi.fn(async () => {}),
      expired: vi.fn(),
    },
    set(next: { gate: string; required: boolean }) {
      snap = next
      listeners.forEach((l) => l())
    },
  }
})
vi.mock('../shared/state/auth', async (original) => ({ ...(await original<typeof import('../shared/state/auth')>()), auth: fake.auth }))

function show(queryClient = new QueryClient()) {
  render(
    <QueryClientProvider client={queryClient}>
      <AuthGate>
        <p>the dashboard</p>
      </AuthGate>
    </QueryClientProvider>,
  )
  return queryClient
}

beforeEach(() => {
  vi.clearAllMocks()
  fake.set({ gate: 'checking', required: false })
})

describe('AuthGate', () => {
  it('waits for the server before showing anything, and asks it', () => {
    show()
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
    expect(screen.queryByText('the dashboard')).not.toBeInTheDocument()
    expect(fake.auth.check).toHaveBeenCalled()
  })

  it('shows the dashboard when no login is needed', () => {
    fake.set({ gate: 'open', required: false })
    show()
    expect(screen.getByText('the dashboard')).toBeInTheDocument()
    expect(screen.queryByTestId('login-password')).not.toBeInTheDocument()
  })

  it('shows the login, and not the dashboard, when the server asks for a password', () => {
    fake.set({ gate: 'login', required: true })
    show()
    expect(screen.getByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.queryByText('the dashboard')).not.toBeInTheDocument()
  })

  it('cannot be submitted empty, and sends what was typed', async () => {
    fake.set({ gate: 'login', required: true })
    show()
    const submit = screen.getByTestId('login-submit')
    expect(submit).toBeDisabled()
    await userEvent.type(screen.getByTestId('login-password'), 'open sesame')
    expect(submit).toBeEnabled()
    await userEvent.click(submit)
    expect(fake.auth.signIn).toHaveBeenCalledWith('open sesame')
  })

  it('says the password is wrong, empties the field and lets the person try again', async () => {
    fake.set({ gate: 'login', required: true })
    fake.auth.signIn.mockRejectedValueOnce(new ApiError(401, 'incorrect password'))
    show()
    await userEvent.type(screen.getByTestId('login-password'), 'nope')
    await userEvent.click(screen.getByTestId('login-submit'))
    expect(await screen.findByTestId('login-error')).toHaveTextContent('That password is not right.')
    expect(screen.getByTestId('login-password')).toHaveValue('')
    await userEvent.type(screen.getByTestId('login-password'), 'again')
    expect(screen.getByTestId('login-submit')).toBeEnabled()
  })

  it('says to wait when too many passwords were tried', async () => {
    fake.set({ gate: 'login', required: true })
    fake.auth.signIn.mockRejectedValueOnce(new ApiError(429, 'too many login attempts; wait a moment'))
    show()
    await userEvent.type(screen.getByTestId('login-password'), 'x')
    await userEvent.click(screen.getByTestId('login-submit'))
    expect(await screen.findByTestId('login-error')).toHaveTextContent('Too many attempts')
  })

  it('says so when the server cannot be reached', async () => {
    fake.set({ gate: 'login', required: true })
    fake.auth.signIn.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    show()
    await userEvent.type(screen.getByTestId('login-password'), 'x')
    await userEvent.click(screen.getByTestId('login-submit'))
    expect(await screen.findByTestId('login-error')).toHaveTextContent('could not be reached')
  })

  it('empties the query cache when the login comes up, so nothing of the session stays in memory', async () => {
    fake.set({ gate: 'open', required: true })
    const queryClient = new QueryClient()
    queryClient.setQueryData(['results'], { secret: true })
    show(queryClient)
    expect(queryClient.getQueryData(['results'])).toBeDefined()
    act(() => fake.set({ gate: 'login', required: true }))
    await waitFor(() => expect(queryClient.getQueryData(['results'])).toBeUndefined())
    expect(screen.queryByText('the dashboard')).not.toBeInTheDocument()
  })

  it('returns to the dashboard after signing in', async () => {
    fake.set({ gate: 'login', required: true })
    show()
    act(() => fake.set({ gate: 'open', required: true }))
    expect(screen.getByText('the dashboard')).toBeInTheDocument()
  })
})
