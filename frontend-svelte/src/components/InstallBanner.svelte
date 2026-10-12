<script lang="ts">
  import { dismissInstall, install, promptInstall } from '../lib/install.svelte'
  import Icon from './Icon.svelte'

  const s = $derived(install.state)
</script>

<!-- Offers to install the app: a button where the browser has an install dialog, a how-to on iPhone and iPad. -->
{#if !s.installed && !s.dismissed && (s.canPrompt || s.showIosHint)}
  <div class="banner install" role="region" aria-label="Install the app" data-testid="install-banner">
    <Icon name="info" />
    {#if s.canPrompt}
      <span><strong>Install Speedtest</strong> to open it from your home screen or desktop, with its own window.</span>
    {:else}
      <span><strong>Install Speedtest:</strong> tap the Share button, then choose <strong>Add to Home Screen</strong>.</span>
    {/if}
    <span class="banner-actions">
      {#if s.canPrompt}
        <button type="button" class="btn primary" onclick={() => void promptInstall()} data-testid="install-app">Install</button>
      {/if}
      <button type="button" class="btn" onclick={dismissInstall} data-testid="install-dismiss">Not now</button>
    </span>
  </div>
{/if}
