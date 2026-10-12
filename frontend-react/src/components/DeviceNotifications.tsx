import { usePush, type PushState } from '../state/usePush'
import { InfoIcon } from './ui'

/** Plain-language explanation for each situation this browser can be in. */
function Explanation({ state, alertsOff }: { state: PushState; alertsOff: boolean }) {
  switch (state.status) {
    case 'loading':
      return <p className="muted">Checking…</p>
    case 'server-off':
      return <p className="muted"><InfoIcon /> Browser notifications are turned off on the server (<code>PUSH_ENABLED=no</code>).</p>
    case 'unreachable':
      return <p className="muted">Couldn’t reach the server to check notifications. Try again when you’re back online.</p>
    case 'blocked':
      switch (state.why) {
        case 'insecure':
          return (
            <p className="muted">
              <InfoIcon /> Browsers only allow notifications on secure (https) pages. Open this app through the https address
              set up in <code>docs/DEPLOY.md</code> (“HTTPS on your network”), then come back here.
            </p>
          )
        case 'install-first':
          return (
            <p className="muted">
              <InfoIcon /> On iPhone and iPad, notifications work only for an app on your Home Screen. Tap the Share button,
              choose <strong>Add to Home Screen</strong>, then open Speedtest from there and turn notifications on.
            </p>
          )
        case 'no-worker':
          return (
            <p className="muted">
              <InfoIcon /> The app’s background service didn’t start, so notifications can’t be turned on yet. If you got here by
              clicking past a certificate warning, trust the certificate first (see “HTTPS on your network” in
              <code> docs/DEPLOY.md</code>), then reload this page.
            </p>
          )
        default:
          return <p className="muted"><InfoIcon /> This browser can’t show notifications from a website.</p>
      }
    case 'denied':
      return (
        <p className="muted">
          <InfoIcon /> Notifications are blocked for this site. Allow them in your browser’s site settings, then reload this page.
        </p>
      )
    case 'off':
      return <p>Get a notification on this device when a test is slow, loses packets, or a device stops testing.</p>
    case 'on':
      return (
        <p>
          <strong>Notifications are on for this device.</strong>
          {alertsOff && <span className="muted"> Alerts are off on the server, so only the test message will arrive until <code>NTFY_TOPIC</code> is set.</span>}
        </p>
      )
  }
}

/** Turn notifications on or off for the device you are using right now. */
export function DeviceNotifications({ alertsOff }: { alertsOff: boolean }) {
  const { state, busy, error, testSent, enable, disable, sendTest } = usePush()
  return (
    <div className="device-notify" data-testid="device-notifications" aria-live="polite">
      <h3>On this device</h3>
      <Explanation state={state} alertsOff={alertsOff} />
      {state.status === 'off' && (
        <button type="button" className="btn primary" onClick={enable} disabled={busy} data-testid="push-enable">
          {busy ? 'Turning on…' : 'Turn on notifications'}
        </button>
      )}
      {state.status === 'on' && (
        <div className="device-notify-actions">
          <button type="button" className="btn" onClick={sendTest} disabled={busy} data-testid="push-test">
            {busy ? 'Sending…' : 'Send a test'}
          </button>
          <button type="button" className="btn" onClick={disable} disabled={busy} data-testid="push-disable">Turn off</button>
        </div>
      )}
      {testSent && <p className="muted" role="status" data-testid="push-test-sent">Test sent. It should arrive in a few seconds.</p>}
      {error && <p className="failed" role="alert" data-testid="push-error">{error}</p>}
    </div>
  )
}
