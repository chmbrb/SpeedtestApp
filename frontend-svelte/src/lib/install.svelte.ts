import { getInstallState, subscribeInstall, type InstallState } from '../shared/state/install'

export { dismissInstall, initInstall, promptInstall } from '../shared/state/install'

/** The shared install logic, as a value Svelte components can read. */
class Install {
  state = $state<InstallState>(getInstallState())
  constructor() {
    subscribeInstall(() => { this.state = getInstallState() })
  }
}

export const install = new Install()
