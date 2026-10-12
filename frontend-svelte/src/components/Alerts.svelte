<script lang="ts">
  import type { ActiveAlert } from '../shared/api/client'
  import { dateTime } from '../shared/lib/format'
  import Icon from './Icon.svelte'

  /** Problems that are happening now. Shown above everything else, because it is the first thing to know. */
  let { alerts }: { alerts: ActiveAlert[] } = $props()
</script>

{#if alerts.length > 0}
  <section class="alerts" aria-label="Active alerts" data-testid="alert-banner">
    <ul>
      {#each alerts as a (`${a.kind}|${a.subject}`)}
        <li class="alert sev-{a.severity}">
          <Icon name="alert" class="alert-icon" />
          <p>
            <strong>{a.title}.</strong> {a.detail}{' '}
            <span class="muted">Since {dateTime(a.since)}.</span>
          </p>
        </li>
      {/each}
    </ul>
  </section>
{/if}
