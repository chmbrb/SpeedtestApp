<script lang="ts">
  import { Push } from '../lib/push.svelte'
  import Icon from './Icon.svelte'

  /** Turn notifications on or off for the device you are using right now. */
  let { alertsOff }: { alertsOff: boolean } = $props()

  const push = new Push()
  const ps = $derived(push.state)
</script>

<div class="device-notify" data-testid="device-notifications" aria-live="polite">
  <h3>On this device</h3>

  {#if ps.status === 'loading'}
    <p class="muted">Checking…</p>
  {:else if ps.status === 'server-off'}
    <p class="muted"><Icon name="info" /> Browser notifications are turned off on the server (<code>PUSH_ENABLED=no</code>).</p>
  {:else if ps.status === 'unreachable'}
    <p class="muted">Couldn’t reach the server to check notifications. Try again when you’re back online.</p>
  {:else if ps.status === 'blocked'}
    {#if ps.why === 'insecure'}
      <p class="muted">
        <Icon name="info" /> Browsers only allow notifications on secure (https) pages. Open this app through the https address
        set up in <code>docs/DEPLOY.md</code> (“HTTPS on your network”), then come back here.
      </p>
    {:else if ps.why === 'install-first'}
      <p class="muted">
        <Icon name="info" /> On iPhone and iPad, notifications work only for an app on your Home Screen. Tap the Share button,
        choose <strong>Add to Home Screen</strong>, then open Speedtest from there and turn notifications on.
      </p>
    {:else if ps.why === 'no-worker'}
      <p class="muted">
        <Icon name="info" /> The app’s background service didn’t start, so notifications can’t be turned on yet. If you got here by
        clicking past a certificate warning, trust the certificate first (see “HTTPS on your network” in
        <code> docs/DEPLOY.md</code>), then reload this page.
      </p>
    {:else}
      <p class="muted"><Icon name="info" /> This browser can’t show notifications from a website.</p>
    {/if}
  {:else if ps.status === 'denied'}
    <p class="muted">
      <Icon name="info" /> Notifications are blocked for this site. Allow them in your browser’s site settings, then reload this page.
    </p>
  {:else if ps.status === 'off'}
    <p>Get a notification on this device when a test is slow, loses packets, or a device stops testing.</p>
  {:else if ps.status === 'on'}
    <p>
      <strong>Notifications are on for this device.</strong>
      {#if alertsOff}<span class="muted"> Alerts are off on the server, so only the test message will arrive until <code>NTFY_TOPIC</code> is set.</span>{/if}
    </p>
  {/if}

  {#if ps.status === 'off'}
    <button type="button" class="btn primary" onclick={push.enable} disabled={push.busy} data-testid="push-enable">
      {push.busy ? 'Turning on…' : 'Turn on notifications'}
    </button>
  {/if}
  {#if ps.status === 'on'}
    <div class="device-notify-actions">
      <button type="button" class="btn" onclick={push.sendTest} disabled={push.busy} data-testid="push-test">
        {push.busy ? 'Sending…' : 'Send a test'}
      </button>
      <button type="button" class="btn" onclick={push.disable} disabled={push.busy} data-testid="push-disable">Turn off</button>
    </div>
  {/if}
  {#if push.testSent}<p class="muted" role="status" data-testid="push-test-sent">Test sent. It should arrive in a few seconds.</p>{/if}
  {#if push.error}<p class="failed" role="alert" data-testid="push-error">{push.error}</p>{/if}
</div>
