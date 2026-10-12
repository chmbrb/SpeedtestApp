import ndt7 from '@m-lab/ndt7'
import downloadWorker from '@m-lab/ndt7/src/ndt7-download-worker.js?url'
import uploadWorker from '@m-lab/ndt7/src/ndt7-upload-worker.js?url'
import type { Ndt7Summary } from '../api/client'

/** M-Lab's data policy, which a person must accept before a browser test (the client library refuses to run otherwise). */
export const MLAB_POLICY_URL = 'https://www.measurementlab.net/privacy/'

export type Ndt7Phase = 'locating' | 'download' | 'upload'
export interface Ndt7Sample { phase: 'download' | 'upload'; elapsedMs: number; mbps: number }

interface ClientMeasurement { ElapsedTime?: number; NumBytes?: number; MeanClientMbps?: number }
interface ServerMeasurement {
  TCPInfo?: { BytesSent?: number; BytesRetrans?: number; BytesReceived?: number; ElapsedTime?: number; MinRTT?: number }
  BBRInfo?: { MinRTT?: number }
}
interface Last { client?: ClientMeasurement; server?: ServerMeasurement }
interface ChosenServer { machine?: string; location?: { city?: string; country?: string } }

const num = (v: unknown): number | null => (typeof v === 'number' && isFinite(v) ? v : null)
const round = (v: number, digits: number) => Math.round(v * 10 ** digits) / 10 ** digits

/**
 * Turn what the M-Lab client reported into the summary the backend stores.
 * - Download speed is the client's own mean over the test.
 * - Upload speed prefers what the *server* received (the browser's count includes data still sitting in its send buffer
 *   and overstates the speed), and falls back to the client's figure.
 * - "Packet loss" is the TCP retransmission rate the server saw while sending (retransmitted bytes / bytes sent).
 * - Latency is the lowest round-trip time the server saw.
 * Unknown values are null, not guessed.
 */
export function buildNdt7Summary(input: {
  startedAt: Date
  finishedAt: Date
  download: Last
  upload: Last
  server: ChosenServer | null
}): Ndt7Summary {
  const { download, upload } = input
  const downloadMbps = num(download.client?.MeanClientMbps)
  if (downloadMbps == null) throw new Error('The download speed could not be measured.')

  const tcp = upload.server?.TCPInfo
  const bytesReceived = num(tcp?.BytesReceived)
  const elapsedUs = num(tcp?.ElapsedTime)
  const serverUpload = bytesReceived != null && elapsedUs != null && elapsedUs > 0 ? (bytesReceived * 8) / elapsedUs : null // bits per µs = Mbit/s
  const uploadMbps = serverUpload ?? num(upload.client?.MeanClientMbps)
  if (uploadMbps == null) throw new Error('The upload speed could not be measured.')

  const dtcp = download.server?.TCPInfo
  const sent = num(dtcp?.BytesSent)
  const retrans = num(dtcp?.BytesRetrans)
  const retransmissionPct = sent != null && sent > 0 && retrans != null ? Math.min(100, Math.max(0, (retrans / sent) * 100)) : null
  const minRttUs = num(dtcp?.MinRTT) ?? num(download.server?.BBRInfo?.MinRTT)

  const s = input.server
  return {
    startedAt: input.startedAt.toISOString(),
    finishedAt: input.finishedAt.toISOString(),
    downloadMbps: round(downloadMbps, 2),
    uploadMbps: round(uploadMbps, 2),
    minRttMs: minRttUs != null && minRttUs > 0 ? round(minRttUs / 1000, 2) : null,
    retransmissionPct: retransmissionPct != null ? round(retransmissionPct, 3) : null,
    server: {
      machine: s?.machine ?? '',
      location: s?.location?.city ?? '',
      country: s?.location?.country ?? '',
    },
    raw: {
      client: { download: download.client ?? null, upload: upload.client ?? null },
      tcpInfo: { download: dtcp ?? null, upload: tcp ?? null },
    },
  }
}

export class Ndt7Error extends Error {}

export const REACH_MLAB =
  'Couldn’t reach M-Lab’s test servers. Check this device’s internet connection and try again.'

/**
 * Run a download then an upload test in this browser. Cancelling (via the signal) takes effect between the two phases and
 * as soon as the current phase ends (about 10 seconds): the M-Lab client has no way to stop a phase part-way.
 * The caller must have shown M-Lab's data policy and had it accepted.
 */
export async function runNdt7(hooks: {
  onPhase: (p: Ndt7Phase) => void
  onSample: (s: Ndt7Sample) => void
}, signal: AbortSignal): Promise<Ndt7Summary> {
  const startedAt = new Date()
  let server: ChosenServer | null = null
  let failure: string | null = null
  const last = { download: {} as Last, upload: {} as Last }

  const config = {
    userAcceptedDataPolicy: true, // only ever reached after the person accepted M-Lab's data policy in the UI
    downloadworkerfile: downloadWorker,
    uploadworkerfile: uploadWorker,
    metadata: { client_name: 'speedtestapp', client_version: '1' },
  }
  const sample = (phase: 'download' | 'upload') => (m: { Source: 'client' | 'server'; Data: unknown }) => {
    if (m.Source !== 'client') return
    const d = m.Data as ClientMeasurement
    const mbps = num(d.MeanClientMbps)
    const t = num(d.ElapsedTime)
    if (mbps != null && t != null) hooks.onSample({ phase, elapsedMs: t * 1000, mbps })
  }
  const callbacks = {
    error: (message: string) => { failure ??= String(message) },
    serverChosen: (s: unknown) => { server = s as ChosenServer },
    downloadMeasurement: sample('download'),
    uploadMeasurement: sample('upload'),
    downloadComplete: (r: { LastClientMeasurement?: unknown; LastServerMeasurement?: unknown }) => {
      last.download = { client: r.LastClientMeasurement as ClientMeasurement, server: r.LastServerMeasurement as ServerMeasurement }
    },
    uploadComplete: (r: { LastClientMeasurement?: unknown; LastServerMeasurement?: unknown }) => {
      last.upload = { client: r.LastClientMeasurement as ClientMeasurement, server: r.LastServerMeasurement as ServerMeasurement }
    },
  }
  const aborted = () => new DOMException('The test was cancelled.', 'AbortError')

  hooks.onPhase('locating')
  const urls = ndt7.discoverServerURLs(config, callbacks)
  urls.catch(() => undefined) // a failure is reported through the phase that awaits it

  hooks.onPhase('download')
  const d = await ndt7.downloadTest(config, callbacks, urls)
  if (signal.aborted) throw aborted()
  if (d !== 0 || failure) throw new Ndt7Error(failure ? friendly(failure) : REACH_MLAB)

  hooks.onPhase('upload')
  const u = await ndt7.uploadTest(config, callbacks, urls)
  if (signal.aborted) throw aborted()
  if (u !== 0 || failure) throw new Ndt7Error(failure ? friendly(failure) : REACH_MLAB)

  try {
    return buildNdt7Summary({ startedAt, finishedAt: new Date(), download: last.download, upload: last.upload, server })
  } catch (e) {
    throw new Ndt7Error((e as Error).message)
  }
}

function friendly(message: string): string {
  if (/fetch|network|locate|load failed/i.test(message)) return REACH_MLAB
  if (/data policy/i.test(message)) return 'M-Lab’s data policy has to be accepted before a test can run.'
  return `The test could not finish (${message}). Try again.`
}
