<script lang="ts">
  import type { Snippet } from 'svelte'
  import Icon from './Icon.svelte'

  /** A button that opens a list of options (single or multiple choice). */
  let { label, summary, multiple = false, options, selected, onToggle, footer, align = 'start' }: {
    label: string
    summary: string
    multiple?: boolean
    options: { value: string; label: string; hint?: string }[]
    selected: string[]
    onToggle: (value: string) => void
    footer?: Snippet
    align?: 'start' | 'end'
  } = $props()

  let open = $state(false)
  let root = $state<HTMLDivElement>()
  const id = `menu-${Math.random().toString(36).slice(2, 9)}`

  $effect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => { if (!root?.contains(e.target as Node)) open = false }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { open = false; root?.querySelector('button')?.focus() } }
    document.addEventListener('pointerdown', onDown)
    document.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('pointerdown', onDown); document.removeEventListener('keydown', onKey) }
  })
</script>

<div class="menu" bind:this={root}>
  <button type="button" class="menu-button" aria-haspopup="true" aria-expanded={open} aria-controls={id} onclick={() => (open = !open)}>
    <span class="menu-label">{label}</span>
    <span class="menu-value">{summary}</span>
    <Icon name="chevron" class="menu-chevron" />
  </button>
  {#if open}
    <div {id} class="menu-panel align-{align}" role="group" aria-label={label}>
      {#if options.length === 0}<p class="menu-empty">Nothing to choose yet.</p>{/if}
      {#each options as o (o.value)}
        {@const on = selected.includes(o.value)}
        <button type="button" class="menu-option" role={multiple ? 'menuitemcheckbox' : 'menuitemradio'} aria-checked={on}
          onclick={() => { onToggle(o.value); if (!multiple) open = false }}>
          <span class="menu-check">{#if on}<Icon name="check" />{/if}</span>
          <span class="menu-option-label">{o.label}</span>
          {#if o.hint}<span class="menu-hint">{o.hint}</span>{/if}
        </button>
      {/each}
      {#if footer}<div class="menu-footer">{@render footer()}</div>{/if}
    </div>
  {/if}
</div>
