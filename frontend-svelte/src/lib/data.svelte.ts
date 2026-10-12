// The questions the dashboard asks the API, each kept fresh. The same ones as frontend-react/src/api/hooks.ts.
import { api, unwrap, type Interval } from '../shared/api/client'
import type { Filters, Window } from '../shared/state/filters'
import { PagedQuery, Query } from './query.svelte'

const REFRESH = 60_000

/** Query parameters shared by every data request, from the active filters and a time window. */
function scope(f: Filters, w: Window) {
  return {
    from: w.from,
    to: w.to,
    provider: f.providers,
    device: f.devices.length ? f.devices : undefined,
    connectionType: f.connection === 'all' ? undefined : [f.connection],
  }
}

export const devices = () =>
  new Query({
    key: () => ['devices'],
    fn: async (signal) => (await unwrap(api.GET('/api/v1/devices', { signal }))).items,
    refetchMs: REFRESH * 5,
  })

export const summary = (f: () => Filters, w: () => Window, enabled: () => boolean = () => true) =>
  new Query({
    key: () => ['summary', f().providers, f().devices, f().connection, w().from, w().to],
    fn: (signal) => unwrap(api.GET('/api/v1/stats/summary', { params: { query: scope(f(), w()) }, signal })),
    refetchMs: REFRESH,
    enabled,
  })

export const timeseries = (f: () => Filters, w: () => Window, interval: () => Interval, timezone: string, enabled: () => boolean) =>
  new Query({
    key: () => ['timeseries', f().providers, f().devices, f().connection, w().from, w().to, interval(), timezone],
    fn: (signal) =>
      unwrap(api.GET('/api/v1/stats/timeseries', {
        params: { query: { ...scope(f(), w()), interval: interval(), timezone, groupBy: ['provider', 'device', 'connectionType'] } },
        signal,
      })),
    refetchMs: REFRESH,
    enabled,
  })

/** The newest results for the filters (not limited by the time range, so "latest" is always the latest). */
export const recent = (f: () => Filters, limit = 30) =>
  new Query({
    key: () => ['recent', f().providers, f().devices, f().connection, limit],
    fn: async (signal) => (await unwrap(api.GET('/api/v1/results', { params: { query: { ...scope(f(), {}), limit } }, signal }))).items,
    refetchMs: REFRESH,
  })

export const resultsPages = (f: () => Filters, w: () => Window, pageSize = 25) =>
  new PagedQuery({
    key: () => ['results', f().providers, f().devices, f().connection, w().from, w().to, pageSize],
    fn: async (cursor, signal) => {
      const page = await unwrap(api.GET('/api/v1/results', { params: { query: { ...scope(f(), w()), limit: pageSize, cursor } }, signal }))
      return { page, next: page.nextCursor ?? undefined }
    },
    refetchMs: REFRESH,
  })

/** Alert status. Cheap to poll, and a failure here must never break the rest of the page. */
export const notifications = () =>
  new Query({
    key: () => ['notifications'],
    fn: (signal) => unwrap(api.GET('/api/v1/notifications', { signal })),
    refetchMs: REFRESH,
    retry: 0,
  })

export const capabilities = () =>
  new Query({
    key: () => ['capabilities'],
    fn: (signal) => unwrap(api.GET('/api/v1/capabilities', { signal })),
    retry: 0,
  })

/** Tests queued or running right now, from anyone. Polls quickly only while something is running. */
export const activeTests = () =>
  new Query({
    key: () => ['tests', 'active'],
    fn: async (signal) => (await unwrap(api.GET('/api/v1/tests', { params: { query: { state: ['queued', 'running'], limit: 10 } }, signal }))).items,
    // poll quickly only while a server test runs; a browser test left open waits out its expiry quietly
    refetchMs: (items) => (items?.some((t) => t.mode === 'server') ? 3_000 : 30_000),
    retry: 0,
  })

export const pushConfig = () =>
  new Query({
    key: () => ['push-config'],
    fn: (signal) => unwrap(api.GET('/api/v1/push/config', { signal })),
    retry: 1,
  })
