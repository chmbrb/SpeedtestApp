<script lang="ts">
  import type { Result } from '../shared/api/client'
  import { colorVar, slotFor } from '../shared/lib/colors'
  import { dateTime, label, mbps, ms, pct, providerName } from '../shared/lib/format'

  let { results, slots, hasMore, loadingMore, onMore, loading, error = null, onRetry, onReset }: {
    results: Result[]
    slots: Map<string, number>
    hasMore: boolean
    loadingMore: boolean
    onMore: () => void
    loading: boolean
    error?: string | null
    onRetry?: () => void
    onReset?: () => void
  } = $props()
</script>

{#if error && results.length === 0}
  <div class="state" role="alert">
    <p><strong>Couldn’t load tests.</strong> {error}</p>
    {#if onRetry}<button type="button" class="btn" onclick={onRetry}>Try again</button>{/if}
  </div>
{:else if results.length === 0}
  <div class="state">
    {#if loading}
      <p class="muted">Loading…</p>
    {:else}
      <p>No tests match these filters.</p>
      {#if onReset}<button type="button" class="btn" onclick={onReset}>Reset filters</button>{/if}
    {/if}
  </div>
{:else}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="table-wrap" tabindex="0" role="region" aria-label="Recent tests, newest first">
    <table class="data results">
      <thead>
        <tr>
          <th scope="col">When</th>
          <th scope="col">Device</th>
          <th scope="col">Provider</th>
          <th scope="col" class="num">Download</th>
          <th scope="col" class="num">Upload</th>
          <th scope="col" class="num">Latency</th>
          <th scope="col" class="num">Loss</th>
          <th scope="col" class="wide">Server</th>
        </tr>
      </thead>
      <tbody>
        {#each results as r (r.id)}
          {@const color = colorVar(slotFor({ provider: r.provider, device: r.device, connectionType: r.connectionType }, slots))}
          <tr>
            <th scope="row"><time datetime={r.testedAt}>{dateTime(r.testedAt)}</time></th>
            <td><span class="key" style:background={color}></span>{r.device} <span class="muted">· {label(r.connectionType)}</span></td>
            <td>{providerName(r.provider)}{#if r.trigger === 'on_demand'}<span class="muted"> · manual</span>{/if}</td>
            <td class="num">{mbps(r.downloadMbps)}<span class="u"> Mbps</span></td>
            <td class="num">{mbps(r.uploadMbps)}<span class="u"> Mbps</span></td>
            <td class="num">{ms(r.latencyMs)}<span class="u"> ms</span></td>
            <td class="num">{#if r.packetLossPct == null}—{:else}{pct(r.packetLossPct)}<span class="u">%</span>{/if}</td>
            <td class="wide muted">{r.server.name ?? '—'}{r.server.location ? `, ${r.server.location}` : ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if hasMore}
    <div class="more">
      <button type="button" class="btn" onclick={onMore} disabled={loadingMore}>
        {loadingMore ? 'Loading…' : 'Show more tests'}
      </button>
    </div>
  {/if}
{/if}
