// @m-lab/ndt7 ships no types. This declares only the parts we use.
declare module '@m-lab/ndt7' {
  interface Ndt7Config {
    userAcceptedDataPolicy?: boolean
    mlabDataPolicyInapplicable?: boolean
    downloadworkerfile?: string
    uploadworkerfile?: string
    metadata?: Record<string, string>
  }
  interface Ndt7Callbacks {
    error?: (message: string) => void
    serverChosen?: (server: unknown) => void
    downloadStart?: (data: unknown) => void
    downloadMeasurement?: (m: { Source: 'client' | 'server'; Data: unknown }) => void
    downloadComplete?: (r: { LastClientMeasurement?: unknown; LastServerMeasurement?: unknown }) => void
    uploadStart?: (data: unknown) => void
    uploadMeasurement?: (m: { Source: 'client' | 'server'; Data: unknown }) => void
    uploadComplete?: (r: { LastClientMeasurement?: unknown; LastServerMeasurement?: unknown }) => void
  }
  const ndt7: {
    discoverServerURLs(config: Ndt7Config, callbacks: Ndt7Callbacks): Promise<Record<string, string>>
    downloadTest(config: Ndt7Config, callbacks: Ndt7Callbacks, urls: Promise<Record<string, string>>): Promise<number>
    uploadTest(config: Ndt7Config, callbacks: Ndt7Callbacks, urls: Promise<Record<string, string>>): Promise<number>
  }
  export default ndt7
}
