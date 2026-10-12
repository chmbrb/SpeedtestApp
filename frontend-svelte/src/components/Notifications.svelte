<script lang="ts">
  import type { NotificationStatus } from '../shared/api/client'
  import { describeRules, eventLabel } from '../shared/lib/alerts'
  import { dateTime } from '../shared/lib/format'
  import DeviceNotifications from './DeviceNotifications.svelte'
  import Icon from './Icon.svelte'

  let { status, loading, failed }: { status: NotificationStatus | undefined; loading: boolean; failed: boolean } = $props()
</script>

<section aria-labelledby="notif-heading" data-testid="notifications">
  <h2 id="notif-heading">Notifications</h2>
  <DeviceNotifications alertsOff={!!status && !status.enabled} />
  {#if failed && !status}
    <p class="muted">Couldn’t load the notification status.</p>
  {:else if !status}
    <p class="muted">{loading ? 'Loading…' : ''}</p>
  {:else if !status.enabled || !status.rules}
    <div class="notif-off">
      <p><Icon name="info" /> <strong>Notifications are off.</strong></p>
      <p class="muted">
        To get alerts on your phone, set <code>NTFY_TOPIC</code> on the server and restart it. The setup steps are in
        <code> backend-go/README.md</code>.
      </p>
    </div>
  {:else}
    <p>Notifications go to your phone through <strong>{status.server}</strong>. You hear about it when:</p>
    <ul class="rules">
      {#each describeRules(status.rules) as line (line)}<li>{line}</li>{/each}
    </ul>

    <h3 class="sent-heading">Recently sent</h3>
    {#if status.recent.length === 0}
      <p class="muted">Nothing sent yet. Everything has been normal since notifications were turned on.</p>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-wrap" tabindex="0" role="region" aria-label="Recently sent notifications">
        <table class="data">
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">Type</th>
              <th scope="col">Message</th>
              <th scope="col">Delivery</th>
            </tr>
          </thead>
          <tbody>
            {#each status.recent as e (e.id)}
              <tr>
                <th scope="row"><time datetime={e.at}>{dateTime(e.at)}</time></th>
                <td>{eventLabel(e.event)}</td>
                <td class="msg"><strong>{e.title}</strong> <span class="muted">{e.message}</span></td>
                <td>{#if e.delivered}Delivered{:else}<span class="failed">Failed: {e.error ?? 'unknown error'}</span>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</section>
