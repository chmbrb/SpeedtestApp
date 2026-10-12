import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api, unwrap, type Interval } from '../shared/api/client'
import type { Filters, Window } from '../shared/state/filters'

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

export function useDevices() {
  return useQuery({
    queryKey: ['devices'],
    queryFn: () => unwrap(api.GET('/api/v1/devices')),
    select: (d) => d.items,
    refetchInterval: REFRESH * 5,
    staleTime: REFRESH,
  })
}

export function useSummary(f: Filters, w: Window, enabled = true) {
  return useQuery({
    queryKey: ['summary', f.providers, f.devices, f.connection, w.from, w.to],
    queryFn: () => unwrap(api.GET('/api/v1/stats/summary', { params: { query: scope(f, w) } })),
    placeholderData: keepPreviousData,
    refetchInterval: REFRESH,
    enabled,
  })
}

export function useTimeseries(f: Filters, w: Window, interval: Interval, timezone: string, enabled = true) {
  return useQuery({
    queryKey: ['timeseries', f.providers, f.devices, f.connection, w.from, w.to, interval, timezone],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/stats/timeseries', {
          params: {
            query: { ...scope(f, w), interval, timezone, groupBy: ['provider', 'device', 'connectionType'] },
          },
        }),
      ),
    placeholderData: keepPreviousData,
    refetchInterval: REFRESH,
    enabled,
  })
}

/** The newest results for the filters (not limited by the time range, so "latest" is always the latest). */
export function useRecent(f: Filters, limit = 30) {
  return useQuery({
    queryKey: ['recent', f.providers, f.devices, f.connection, limit],
    queryFn: () => unwrap(api.GET('/api/v1/results', { params: { query: { ...scope(f, {}), limit } } })),
    select: (d) => d.items,
    placeholderData: keepPreviousData,
    refetchInterval: REFRESH,
  })
}

export function useResultsPages(f: Filters, w: Window, pageSize = 25) {
  return useInfiniteQuery({
    queryKey: ['results', f.providers, f.devices, f.connection, w.from, w.to, pageSize],
    queryFn: ({ pageParam }) =>
      unwrap(api.GET('/api/v1/results', { params: { query: { ...scope(f, w), limit: pageSize, cursor: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    placeholderData: keepPreviousData,
    refetchInterval: REFRESH,
  })
}

/** Alert status. Cheap to poll, and a failure here must never break the rest of the page. */
export function useNotifications() {
  return useQuery({
    queryKey: ['notifications'],
    queryFn: () => unwrap(api.GET('/api/v1/notifications')),
    refetchInterval: REFRESH,
    retry: 0,
  })
}

export function useCapabilities() {
  return useQuery({
    queryKey: ['capabilities'],
    queryFn: () => unwrap(api.GET('/api/v1/capabilities')),
    staleTime: 60_000,
    retry: 0,
  })
}

/** Tests queued or running right now, from anyone. Polls quickly only while something is running. */
export function useActiveTests() {
  return useQuery({
    queryKey: ['tests', 'active'],
    queryFn: () => unwrap(api.GET('/api/v1/tests', { params: { query: { state: ['queued', 'running'], limit: 10 } } })),
    select: (d) => d.items,
    // poll quickly only while a server test runs; a browser test left open waits out its expiry quietly
    refetchInterval: (q) => (q.state.data?.items.some((t) => t.mode === 'server') ? 3_000 : 30_000),
    retry: 0,
  })
}
