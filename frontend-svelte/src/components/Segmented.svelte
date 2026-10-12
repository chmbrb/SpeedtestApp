<script lang="ts" generics="T extends string">
  import Icon, { type IconName } from './Icon.svelte'

  /** A few mutually exclusive choices, all visible. */
  let { label, caption = false, value, options, onChange }: {
    label: string
    /** Visible label shown before the options (the accessible name is always `label`). */
    caption?: boolean
    value: T
    options: { value: T; label: string; icon?: IconName }[]
    onChange: (v: T) => void
  } = $props()
</script>

<div class="segmented-field">
  {#if caption}<span class="menu-label" aria-hidden="true">{label}</span>{/if}
  <div role="radiogroup" aria-label={label} class="segmented">
    {#each options as o (o.value)}
      <button type="button" role="radio" aria-checked={o.value === value} class="seg" onclick={() => onChange(o.value)}>
        {#if o.icon}<Icon name={o.icon} />{/if}
        <span>{o.label}</span>
      </button>
    {/each}
  </div>
</div>
