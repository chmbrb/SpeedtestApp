import { useSyncExternalStore } from 'react'
import { getInstallState, subscribeInstall, type InstallState } from '../shared/state/install'

export { dismissInstall, initInstall, promptInstall } from '../shared/state/install'
export type { InstallState }

export function useInstall(): InstallState {
  return useSyncExternalStore(subscribeInstall, getInstallState, getInstallState)
}
