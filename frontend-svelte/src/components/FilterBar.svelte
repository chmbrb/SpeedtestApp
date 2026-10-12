<script lang="ts">
  import type { Device } from '../shared/api/client'
  import { count } from '../shared/lib/format'
  import { INTERVALS, RANGES, defaultFilters, type Filters } from '../shared/state/filters'
  import Menu from './Menu.svelte'
  import Segmented from './Segmented.svelte'

  const PROVIDER_CHOICES = [
    { value: 'ookla', label: 'Ookla' },
    { value: 'ndt7', label: 'NDT7' },
    { value: 'both', label: 'Both' },
  ] as const
  type ProviderChoice = (typeof PROVIDER_CHOICES)[number]['value']

  let { filters, update, reset, devices }: {
    filters: Filters
    update: (patch: Partial<Filters>) => void
    reset: () => void
    devices: Device[]
  } = $props()

  const providerChoice = (f: Filters): ProviderChoice => (f.providers.length > 1 ? 'both' : ((f.providers[0] as ProviderChoice | undefined) ?? 'ookla'))
  const changed = $derived(JSON.stringify(filters) !== JSON.stringify(defaultFilters))
  const range = $derived(RANGES.find((r) => r.key === filters.range))
  const interval = $derived(INTERVALS.find((i) => i.key === filters.interval))
  const deviceSummary = $derived(
    filters.devices.length === 0 ? 'All' : filters.devices.length === 1 ? (filters.devices[0] as string) : `${filters.devices.length} selected`,
  )
</script>

<div class="filters" role="region" aria-label="Filters">
  <Menu label="Period" summary={range?.label ?? ''} selected={[filters.range]}
    options={RANGES.map((r) => ({ value: r.key, label: r.label }))}
    onToggle={(v) => update({ range: v as Filters['range'] })} />

  <Menu label="Devices" summary={deviceSummary} multiple selected={filters.devices}
    options={devices.map((d) => ({ value: d.name, label: d.name, hint: `${count(d.testCount)} tests` }))}
    onToggle={(v) => update({ devices: filters.devices.includes(v) ? filters.devices.filter((d) => d !== v) : [...filters.devices, v] })}>
    {#snippet footer()}
      {#if filters.devices.length > 0}
        <button type="button" class="link" onclick={() => update({ devices: [] })}>Show all devices</button>
      {/if}
    {/snippet}
  </Menu>

  <Segmented label="Connection" caption value={filters.connection}
    options={[{ value: 'all', label: 'All' }, { value: 'wired', label: 'Wired' }, { value: 'wireless', label: 'Wireless' }]}
    onChange={(connection) => update({ connection })} />

  <Segmented label="Provider" caption value={providerChoice(filters)} options={[...PROVIDER_CHOICES]}
    onChange={(c) => update({ providers: c === 'both' ? ['ndt7', 'ookla'] : [c] })} />

  <Menu label="Detail" summary={interval?.label ?? ''} selected={[filters.interval]} align="end"
    options={INTERVALS.map((i) => ({ value: i.key, label: i.label }))}
    onToggle={(v) => update({ interval: v as Filters['interval'] })} />

  {#if changed}<button type="button" class="link reset" onclick={reset}>Reset filters</button>{/if}
</div>
