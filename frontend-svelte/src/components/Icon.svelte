<script lang="ts">
  /** 16px icons, 1.75 stroke, currentColor. */
  export type IconName = 'check' | 'chevron' | 'arrowUp' | 'arrowDown' | 'minus' | 'alert' | 'ok' | 'info' | 'moon' | 'sun' | 'auto' | 'offline'
  let { name, size = 16, class: className = undefined }: { name: IconName; size?: number; class?: string } = $props()
</script>

<svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75"
  stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class={className}>
  {#if name === 'check'}<path d="M3 8.5l3.2 3.2L13 4.8" />
  {:else if name === 'chevron'}<path d="M4 6l4 4 4-4" />
  {:else if name === 'arrowUp'}<path d="M8 13V3M3.5 7.5L8 3l4.5 4.5" />
  {:else if name === 'arrowDown'}<path d="M8 3v10M3.5 8.5L8 13l4.5-4.5" />
  {:else if name === 'minus'}<path d="M3.5 8h9" />
  {:else if name === 'alert'}<path d="M8 2.2l6.2 11H1.8z" /><path d="M8 6.8v3M8 12v.1" />
  {:else if name === 'ok'}<circle cx="8" cy="8" r="6" /><path d="M5.4 8.2l1.8 1.8 3.4-3.6" />
  {:else if name === 'info'}<circle cx="8" cy="8" r="6" /><path d="M8 7.4v3.4M8 5.2v.1" />
  {:else if name === 'moon'}<path d="M13.2 9.6A5.6 5.6 0 016.4 2.8a5.6 5.6 0 106.8 6.8z" />
  {:else if name === 'sun'}<circle cx="8" cy="8" r="2.6" /><path d="M8 1.6v1.4M8 13v1.4M1.6 8H3M13 8h1.4M3.5 3.5l1 1M11.5 11.5l1 1M3.5 12.5l1-1M11.5 4.5l1-1" />
  {:else if name === 'auto'}<circle cx="8" cy="8" r="5.6" /><path d="M8 2.4v11.2" /><path d="M8 2.4a5.6 5.6 0 010 11.2z" fill="currentColor" />
  {:else if name === 'offline'}<path d="M2 6.2a9 9 0 0112 0M4.4 8.8a5.6 5.6 0 017.2 0" /><path d="M8 12.2v.1" /><path d="M2.5 2.5l11 11" />
  {/if}
</svg>
