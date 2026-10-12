<script lang="ts">
  import type { Snippet } from 'svelte'
  import { theme } from '../lib/theme.svelte'
  import Segmented from './Segmented.svelte'

  let { actions }: { actions?: Snippet } = $props()
</script>

<header class="topbar">
  <a class="brand" href="/" aria-label="Speedtest, home">
    <!-- A small mark: a rising line inside a ring, echoing the app icon. -->
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="8" fill="var(--ink)" />
      <path d="M6 22l6-7 5 4 9-11" fill="none" stroke="var(--plane)" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
    <span>Speedtest</span>
  </a>
  <div class="topbar-right">
    {#if actions}{@render actions()}{/if}
    <Segmented label="Theme" value={theme.choice} onChange={theme.set}
      options={[
        { value: 'dark', label: 'Dark', icon: 'moon' },
        { value: 'light', label: 'Light', icon: 'sun' },
        { value: 'auto', label: 'Auto', icon: 'auto' },
      ]} />
  </div>
</header>
