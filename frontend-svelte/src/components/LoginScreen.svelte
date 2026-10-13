<script lang="ts">
  import { auth, loginErrorMessage } from '../shared/state/auth'

  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (busy || !password) return
    busy = true
    error = ''
    try {
      await auth.signIn(password) // on success this screen goes away
    } catch (err) {
      error = loginErrorMessage(err)
      password = ''
      busy = false
    }
  }
</script>

<main class="auth">
  <form class="auth-card" onsubmit={submit}>
    <div class="auth-brand">
      <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true">
        <rect width="32" height="32" rx="8" fill="var(--ink)" />
        <path d="M6 22l6-7 5 4 9-11" fill="none" stroke="var(--plane)" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <span>Speedtest</span>
    </div>
    <h1>Sign in</h1>
    <!-- Password managers want to see a user name; there is only one shared password, so it is a fixed, hidden one. -->
    <input class="sr-only" type="text" name="username" autocomplete="username" value="speedtest" readonly tabindex="-1" aria-hidden="true" />
    <label class="field">
      <span>Password</span>
      <!-- svelte-ignore a11y_autofocus -->
      <input type="password" name="password" autocomplete="current-password" autofocus required bind:value={password} data-testid="login-password" />
    </label>
    <p class="auth-error" role="alert" data-testid="login-error">{error}</p>
    <button type="submit" class="btn primary" disabled={busy || !password} data-testid="login-submit">
      {busy ? 'Signing in…' : 'Sign in'}
    </button>
  </form>
</main>
