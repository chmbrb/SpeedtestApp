import { auth, loginErrorMessage } from '../shared/state/auth'

// Where the dashboard login stands (see shared/state/auth.ts), as reactive state.
class AuthState {
  #snap = $state(auth.getSnapshot())

  constructor() {
    auth.subscribe(() => { this.#snap = auth.getSnapshot() })
  }

  get gate() { return this.#snap.gate }
  /** The server has a login, so there is something to sign out of. */
  get required() { return this.#snap.required }
}

export const authState = new AuthState()

/** Signs out; if the server could not be told, the person is still signed in and is told so. */
export async function signOut(): Promise<void> {
  try {
    await auth.signOut()
  } catch (e) {
    window.alert(`You are still signed in. ${loginErrorMessage(e)}`)
  }
}
