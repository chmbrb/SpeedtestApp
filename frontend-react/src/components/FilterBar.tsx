import type { Device } from '../shared/api/client'
import { INTERVALS, RANGES, defaultFilters, type Filters } from '../shared/state/filters'
import { Menu, Segmented } from './ui'
import { count } from '../shared/lib/format'

const PROVIDER_CHOICES = [
  { value: 'ookla', label: 'Ookla' },
  { value: 'ndt7', label: 'NDT7' },
  { value: 'both', label: 'Both' },
] as const
type ProviderChoice = (typeof PROVIDER_CHOICES)[number]['value']

const providerChoice = (f: Filters): ProviderChoice => (f.providers.length > 1 ? 'both' : (f.providers[0] ?? 'ookla'))

export function FilterBar({ filters, update, reset, devices }: {
  filters: Filters
  update: (patch: Partial<Filters>) => void
  reset: () => void
  devices: Device[]
}) {
  const changed = JSON.stringify(filters) !== JSON.stringify(defaultFilters)
  const range = RANGES.find((r) => r.key === filters.range)
  const interval = INTERVALS.find((i) => i.key === filters.interval)
  const deviceSummary = filters.devices.length === 0 ? 'All'
    : filters.devices.length === 1 ? (filters.devices[0] as string) : `${filters.devices.length} selected`

  return (
    <div className="filters" role="region" aria-label="Filters">
      <Menu label="Period" summary={range?.label ?? ''} selected={[filters.range]}
        options={RANGES.map((r) => ({ value: r.key, label: r.label }))}
        onToggle={(v) => update({ range: v as Filters['range'] })} />

      <Menu label="Devices" summary={deviceSummary} multiple selected={filters.devices}
        options={devices.map((d) => ({ value: d.name, label: d.name, hint: `${count(d.testCount)} tests` }))}
        onToggle={(v) => update({ devices: filters.devices.includes(v) ? filters.devices.filter((d) => d !== v) : [...filters.devices, v] })}
        footer={filters.devices.length > 0 && <button type="button" className="link" onClick={() => update({ devices: [] })}>Show all devices</button>} />

      <Segmented label="Connection" caption value={filters.connection}
        options={[{ value: 'all', label: 'All' }, { value: 'wired', label: 'Wired' }, { value: 'wireless', label: 'Wireless' }]}
        onChange={(connection) => update({ connection })} />

      <Segmented label="Provider" caption value={providerChoice(filters)} options={[...PROVIDER_CHOICES]}
        onChange={(c) => update({ providers: c === 'both' ? ['ndt7', 'ookla'] : [c] })} />

      <Menu label="Detail" summary={interval?.label ?? ''} selected={[filters.interval]} align="end"
        options={INTERVALS.map((i) => ({ value: i.key, label: i.label }))}
        onToggle={(v) => update({ interval: v as Filters['interval'] })} />

      {changed && <button type="button" className="link reset" onClick={reset}>Reset filters</button>}
    </div>
  )
}
