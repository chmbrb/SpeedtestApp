<script lang="ts">
  import type { Result } from '../shared/api/client'
  import { ago, label, mbps, providerName } from '../shared/lib/format'
  import { judgeSpeed, type Tone } from '../shared/lib/stats'
  import Icon, { type IconName } from './Icon.svelte'
  import Sparkline from './Sparkline.svelte'

  let { latest, recent, typical, now, accent, loading, error = null }: {
    latest: Result | undefined
    recent: Result[]
    typical: number | null | undefined
    now: number
    accent: string
    loading: boolean
    error?: string | null
  } = $props()

  const TONE_ICON: Record<Tone, IconName> = { good: 'ok', warning: 'alert', critical: 'alert', neutral: 'info' }
  const verdict = $derived(latest ? judgeSpeed(latest.downloadMbps, typical, mbps) : null)
</script>

<section class="hero" aria-labelledby="hero-label">
  {#if !latest || !verdict}
    <div class="hero-main">
      <p id="hero-label" class="hero-label">Latest download</p>
      {#if error}
        <p role="alert"><strong>Couldn’t load results.</strong> {error}</p>
      {:else if loading}
        <p class="muted">Loading…</p>
      {:else}
        <p class="hero-empty">No tests yet for these filters.</p>
      {/if}
    </div>
  {:else}
    <div class="hero-main">
      <p id="hero-label" class="hero-label">
        Latest download <span class="hero-when">· {ago(Date.parse(latest.testedAt), now)}</span>
      </p>
      <p class="hero-figure" data-testid="hero-figure">
        <span class="hero-number">{mbps(latest.downloadMbps)}</span>
        <span class="hero-unit">Mbps</span>
      </p>
      <p class="verdict tone-{verdict.tone}" data-testid="verdict">
        <Icon name={TONE_ICON[verdict.tone]} class="verdict-icon" />
        <span><strong>{verdict.title}.</strong> {verdict.detail}</span>
      </p>
      <p class="hero-meta">
        {latest.device}, {label(latest.connectionType)} · {providerName(latest.provider)}
        {latest.server.name ? ` · ${latest.server.name}${latest.server.location ? `, ${latest.server.location}` : ''}` : ''}
      </p>
    </div>
    <Sparkline results={recent} {accent} />
  {/if}
</section>
