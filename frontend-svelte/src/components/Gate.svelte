<script lang="ts">
  import App from '../App.svelte'
  import { authState } from '../lib/auth.svelte'
  import { auth } from '../shared/state/auth'
  import LoginScreen from './LoginScreen.svelte'

  // Ask the server once, when the page opens. The dashboard (and so every request it makes) only exists while the gate is open,
  // and its data lives in its components, so closing the gate leaves nothing of the session in memory.
  $effect(() => { void auth.check() })
</script>

{#if authState.gate === 'checking'}
  <div class="auth-wait" role="status">Loading…</div>
{:else if authState.gate === 'login'}
  <LoginScreen />
{:else}
  <App />
{/if}
