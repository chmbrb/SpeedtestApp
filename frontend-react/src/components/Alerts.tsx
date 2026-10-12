import type { ActiveAlert, NotificationStatus } from '../shared/api/client'
import { dateTime } from '../shared/lib/format'
import { describeRules, eventLabel } from '../shared/lib/alerts'
import { DeviceNotifications } from './DeviceNotifications'
import { AlertIcon, InfoIcon } from './ui'

/** Problems that are happening now. Shown above everything else, because it is the first thing to know. */
export function AlertBanner({ alerts }: { alerts: ActiveAlert[] }) {
  if (alerts.length === 0) return null
  return (
    <section className="alerts" aria-label="Active alerts" data-testid="alert-banner">
      <ul>
        {alerts.map((a) => (
          <li key={`${a.kind}|${a.subject}`} className={`alert sev-${a.severity}`}>
            <AlertIcon className="alert-icon" />
            <p>
              <strong>{a.title}.</strong> {a.detail}{' '}
              <span className="muted">Since {dateTime(a.since)}.</span>
            </p>
          </li>
        ))}
      </ul>
    </section>
  )
}

export function NotificationsSection({ status, loading, failed }: {
  status: NotificationStatus | undefined
  loading: boolean
  failed: boolean
}) {
  return (
    <section aria-labelledby="notif-heading" data-testid="notifications">
      <h2 id="notif-heading">Notifications</h2>
      <DeviceNotifications alertsOff={!!status && !status.enabled} />
      {failed && !status ? (
        <p className="muted">Couldn’t load the notification status.</p>
      ) : !status ? (
        <p className="muted">{loading ? 'Loading…' : ''}</p>
      ) : !status.enabled || !status.rules ? (
        <div className="notif-off">
          <p><InfoIcon /> <strong>Notifications are off.</strong></p>
          <p className="muted">
            To get alerts on your phone, set <code>NTFY_TOPIC</code> on the server and restart it. The setup steps are in
            <code> backend-go/README.md</code>.
          </p>
        </div>
      ) : (
        <>
          <p>Notifications go to your phone through <strong>{status.server}</strong>. You hear about it when:</p>
          <ul className="rules">
            {describeRules(status.rules).map((line) => <li key={line}>{line}</li>)}
          </ul>

          <h3 className="sent-heading">Recently sent</h3>
          {status.recent.length === 0 ? (
            <p className="muted">Nothing sent yet. Everything has been normal since notifications were turned on.</p>
          ) : (
            <div className="table-wrap" tabIndex={0} role="region" aria-label="Recently sent notifications">
              <table className="data">
                <thead>
                  <tr>
                    <th scope="col">When</th>
                    <th scope="col">Type</th>
                    <th scope="col">Message</th>
                    <th scope="col">Delivery</th>
                  </tr>
                </thead>
                <tbody>
                  {status.recent.map((e) => (
                    <tr key={e.id}>
                      <th scope="row"><time dateTime={e.at}>{dateTime(e.at)}</time></th>
                      <td>{eventLabel(e.event)}</td>
                      <td className="msg"><strong>{e.title}</strong> <span className="muted">{e.message}</span></td>
                      <td>{e.delivered ? 'Delivered' : <span className="failed">Failed: {e.error ?? 'unknown error'}</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  )
}
