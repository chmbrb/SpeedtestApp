import { useSyncExternalStore } from 'react'
import { auth, loginErrorMessage, type AuthSnapshot } from '../shared/state/auth'

/** Where the dashboard login stands (see shared/state/auth.ts). */
export function useAuth(): AuthSnapshot {
  return useSyncExternalStore(auth.subscribe, auth.getSnapshot)
}

/** Signs out; if the server could not be told, the person is still signed in and is told so. */
export async function signOut(): Promise<void> {
  try {
    await auth.signOut()
  } catch (e) {
    window.alert(`You are still signed in. ${loginErrorMessage(e)}`)
  }
}
