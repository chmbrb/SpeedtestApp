import createClient from 'openapi-fetch'
import type { components, paths } from './schema'

type S = components['schemas']
export type Result = S['Result']
export type Summary = S['Summary']
export type Timeseries = S['Timeseries']
export type Device = S['Device']
export type Stat = S['Stat']
export type Provider = S['Provider']
export type NotificationStatus = S['NotificationStatus']
export type ActiveAlert = S['ActiveAlert']
export type NotificationEvent = S['NotificationEvent']
export type NotificationRules = S['NotificationRules']
export type Capabilities = S['Capabilities']
export type TestRun = S['Test']
export type Ndt7Summary = S['Ndt7Summary']
export type ConnectionType = S['ConnectionType']
export type Interval = 'raw' | 'hour' | 'day' | 'week' | 'month' | 'quarter' | 'year'
export type Series = Timeseries['series'][number]
export type Point = Series['points'][number]

export const api = createClient<paths>({
  baseUrl: import.meta.env.VITE_API_BASE ?? '',
  // Comma-separated lists (?provider=ookla,ndt7), as the contract specifies.
  querySerializer: { array: { style: 'form', explode: false } },
})

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

function failure(status: number, error: unknown): ApiError {
  const problem = error as { detail?: string; title?: string } | undefined
  return new ApiError(status, problem?.detail ?? problem?.title ?? `Request failed (${status})`)
}

/** Unwrap an openapi-fetch response: data on success, an ApiError carrying the problem detail otherwise. */
export async function unwrap<T>(
  call: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await call
  if (data !== undefined && response.ok) return data
  throw failure(response.status, error)
}

/** For calls that answer 204 No Content: success is the status, there is no body to unwrap. */
export async function expectOk(call: Promise<{ error?: unknown; response: Response }>): Promise<void> {
  const { error, response } = await call
  if (!response.ok) throw failure(response.status, error)
}
