import type { Device } from '../api/client'

/** Identity of one plotted series. */
export interface SeriesKey {
  provider?: string
  device?: string
  connectionType?: string
}

export const keyId = (k: SeriesKey) => `${k.provider ?? ''}|${k.device ?? ''}|${k.connectionType ?? ''}`

export const MAX_SERIES = 8

/**
 * Stable color slots. A series' color depends only on the full list of devices, never on which filters are
 * active, so filtering never repaints the survivors. Slot order follows the validated categorical order
 * (--series-1 … --series-8 in styles.css); there is no 9th color, so extra series are not plotted.
 */
export function buildColorSlots(devices: Device[]): Map<string, number> {
  const ids: string[] = []
  for (const d of [...devices].sort((a, b) => a.name.localeCompare(b.name))) {
    for (const c of [...d.connectionTypes].sort()) {
      for (const p of [...d.providers].sort()) ids.push(keyId({ provider: p, device: d.name, connectionType: c }))
    }
  }
  return new Map(ids.map((id, i) => [id, i]))
}

/** Slot lookup that tolerates group-by subsets: a key matches the first full key containing its fields. */
export function slotFor(key: SeriesKey, slots: Map<string, number>): number | null {
  const exact = slots.get(keyId(key))
  if (exact !== undefined) return exact < MAX_SERIES ? exact : null
  for (const [id, slot] of slots) {
    const [p, d, c] = id.split('|')
    if ((!key.provider || key.provider === p) && (!key.device || key.device === d) && (!key.connectionType || key.connectionType === c)) {
      return slot < MAX_SERIES ? slot : null
    }
  }
  return null
}

export const colorVar = (slot: number | null) => (slot === null ? 'var(--muted)' : `var(--series-${slot + 1})`)
