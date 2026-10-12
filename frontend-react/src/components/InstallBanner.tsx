import { dismissInstall, promptInstall, useInstall } from '../state/install'
import { InfoIcon } from './ui'

/** Offers to install the app: a button where the browser has an install dialog, a how-to on iPhone and iPad. */
export function InstallBanner() {
  const { canPrompt, showIosHint, installed, dismissed } = useInstall()
  if (installed || dismissed || !(canPrompt || showIosHint)) return null
  return (
    <div className="banner install" role="region" aria-label="Install the app" data-testid="install-banner">
      <InfoIcon />
      {canPrompt ? (
        <span><strong>Install Speedtest</strong> to open it from your home screen or desktop, with its own window.</span>
      ) : (
        <span><strong>Install Speedtest:</strong> tap the Share button, then choose <strong>Add to Home Screen</strong>.</span>
      )}
      <span className="banner-actions">
        {canPrompt && <button type="button" className="btn primary" onClick={() => void promptInstall()} data-testid="install-app">Install</button>}
        <button type="button" className="btn" onClick={dismissInstall} data-testid="install-dismiss">Not now</button>
      </span>
    </div>
  )
}
