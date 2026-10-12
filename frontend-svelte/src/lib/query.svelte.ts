// Polling queries, with just the behaviour the dashboard relies on (the React app gets these from TanStack Query):
//  - the data of the previous question stays on screen while a new one loads (no flashing empty charts)
//  - refresh on a timer, and when the tab becomes visible again after a while
//  - one retry for a failed request, none for the optional parts of the page
//  - `invalidate('results')` refreshes every query whose key starts with 'results'
// Call these while a component is being set up: they use $effect, which needs a component to belong to.

const STALE_MS = 30_000
const REFRESH_FAILED_MS = 1_000

type Registered = { name: string; refetch: () => void }
const registry = new Set<Registered>()

/** Refresh every query whose first key part is one of `names`. */
export function invalidate(...names: string[]) {
  for (const q of registry) if (names.includes(q.name)) q.refetch()
}

export interface QueryOptions<T> {
  /** The question. Its first element names the query for `invalidate`; a change of any part asks again. */
  key: () => unknown[]
  fn: (signal: AbortSignal) => Promise<T>
  /** Milliseconds between refreshes, or false for none. May depend on the data (poll fast only while something runs). */
  refetchMs?: number | false | ((data: T | undefined) => number | false)
  enabled?: () => boolean
  /** How many times to try again after a failure. Default 1. */
  retry?: number
}

export class Query<T> {
  data = $state<T | undefined>(undefined)
  error = $state<unknown>(null)
  fetching = $state(false)
  /** True until the first answer or failure: "Loading…" is for this, not for a refresh. */
  loading = $state(false)

  #o: QueryOptions<T>
  #seq = 0
  #ctrl: AbortController | null = null
  #lastKey = ''
  #lastAt = 0

  constructor(o: QueryOptions<T>) {
    this.#o = o
    const entry: Registered = { name: String(o.key()[0] ?? ''), refetch: () => void this.refetch() }
    $effect(() => {
      registry.add(entry)
      return () => { registry.delete(entry) }
    })
    $effect(() => {
      const enabled = o.enabled ? o.enabled() : true
      const key = JSON.stringify(o.key())
      if (!enabled) { this.loading = false; return }
      if (key !== this.#lastKey) {
        this.#lastKey = key
        this.loading = this.data === undefined
        void this.#run()
      }
      let timer: ReturnType<typeof setTimeout> | undefined
      const schedule = () => {
        const every = typeof o.refetchMs === 'function' ? o.refetchMs(this.data) : o.refetchMs
        if (!every) return
        timer = setTimeout(async () => { await this.#run(); schedule() }, every)
      }
      schedule()
      const onVisible = () => { if (document.visibilityState === 'visible' && Date.now() - this.#lastAt > STALE_MS) void this.#run() }
      document.addEventListener('visibilitychange', onVisible)
      return () => { clearTimeout(timer); document.removeEventListener('visibilitychange', onVisible); this.#ctrl?.abort() }
    })
  }

  async refetch() {
    if (this.#lastKey) await this.#run()
  }

  async #run() {
    this.#ctrl?.abort()
    const ctrl = (this.#ctrl = new AbortController())
    const mine = ++this.#seq
    this.fetching = true
    const tries = 1 + (this.#o.retry ?? 1)
    for (let attempt = 1; attempt <= tries; attempt++) {
      try {
        const out = await this.#o.fn(ctrl.signal)
        if (mine !== this.#seq) return // a newer question was asked meanwhile
        this.data = out
        this.error = null
        this.#lastAt = Date.now()
        break
      } catch (e) {
        if (mine !== this.#seq || ctrl.signal.aborted) return
        if (attempt === tries) { this.error = e; break }
        await new Promise((r) => setTimeout(r, REFRESH_FAILED_MS))
        if (mine !== this.#seq) return
      }
    }
    if (mine === this.#seq) { this.fetching = false; this.loading = false }
  }
}

export interface PagedOptions<T> {
  key: () => unknown[]
  fn: (cursor: string | undefined, signal: AbortSignal) => Promise<{ page: T; next: string | undefined }>
  refetchMs?: number
}

/** A list that continues from the last page's cursor ("show more"). A refresh re-reads the pages already shown. */
export class PagedQuery<T> {
  pages = $state<T[]>([])
  error = $state<unknown>(null)
  fetching = $state(false)
  fetchingNext = $state(false)
  loading = $state(false)
  hasNext = $state(false)

  #o: PagedOptions<T>
  #cursors: (string | undefined)[] = [undefined]
  #lastKey = ''
  #seq = 0
  #ctrl: AbortController | null = null
  #next: string | undefined

  constructor(o: PagedOptions<T>) {
    this.#o = o
    const entry: Registered = { name: String(o.key()[0] ?? ''), refetch: () => void this.#reload() }
    $effect(() => {
      registry.add(entry)
      return () => { registry.delete(entry) }
    })
    $effect(() => {
      const key = JSON.stringify(o.key())
      if (key !== this.#lastKey) {
        this.#lastKey = key
        this.#cursors = [undefined]
        this.loading = this.pages.length === 0
        void this.#reload(1)
      }
      let timer: ReturnType<typeof setTimeout> | undefined
      const schedule = () => {
        if (!o.refetchMs) return
        timer = setTimeout(async () => { await this.#reload(); schedule() }, o.refetchMs)
      }
      schedule()
      return () => { clearTimeout(timer); this.#ctrl?.abort() }
    })
  }

  /** Read again the pages that are showing (or the first one). */
  async #reload(minPages = 0) {
    this.#ctrl?.abort()
    const ctrl = (this.#ctrl = new AbortController())
    const mine = ++this.#seq
    this.fetching = true
    const want = Math.max(minPages, this.#cursors.length)
    const out: T[] = []
    const cursors: (string | undefined)[] = [undefined]
    let cursor: string | undefined
    try {
      for (let i = 0; i < want; i++) {
        const { page, next } = await this.#o.fn(cursor, ctrl.signal)
        out.push(page)
        this.#next = next
        if (!next) break
        cursor = next
        if (i + 1 < want) cursors.push(next)
      }
      if (mine !== this.#seq) return
      this.pages = out
      this.#cursors = cursors
      this.hasNext = !!this.#next
      this.error = null
    } catch (e) {
      if (mine !== this.#seq || ctrl.signal.aborted) return
      this.error = e
    }
    if (mine === this.#seq) { this.fetching = false; this.loading = false }
  }

  async fetchNext() {
    if (!this.#next || this.fetchingNext) return
    this.fetchingNext = true
    const cursor = this.#next
    try {
      const { page, next } = await this.#o.fn(cursor, new AbortController().signal)
      this.pages = [...this.pages, page]
      this.#cursors = [...this.#cursors, cursor]
      this.#next = next
      this.hasNext = !!next
      this.error = null
    } catch (e) {
      this.error = e
    } finally {
      this.fetchingNext = false
    }
  }
}
