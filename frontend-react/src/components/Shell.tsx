import { useEffect, useState, type ReactNode } from 'react'
import type { ThemeChoice } from '../state/theme'
import { signOut, useAuth } from '../state/useAuth'
import { AutoIcon, MoonIcon, OfflineIcon, SunIcon, Segmented } from './ui'

export function useOnline(): boolean {
  const [online, setOnline] = useState(() => navigator.onLine)
  useEffect(() => {
    const on = () => setOnline(true)
    const off = () => setOnline(false)
    window.addEventListener('online', on)
    window.addEventListener('offline', off)
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off) }
  }, [])
  return online
}

/** A small mark: a rising line inside a ring, echoing the app icon. */
export function Mark() {
  return (
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="8" fill="var(--ink)" />
      <path d="M6 22l6-7 5 4 9-11" fill="none" stroke="var(--plane)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export function Header({ theme, onTheme, actions }: { theme: ThemeChoice; onTheme: (t: ThemeChoice) => void; actions?: ReactNode }) {
  const { required } = useAuth()
  return (
    <header className="topbar">
      <a className="brand" href="/" aria-label="Speedtest, home">
        <Mark />
        <span>Speedtest</span>
      </a>
      <div className="topbar-right">
        {actions}
        {required && <button type="button" className="btn" onClick={signOut} data-testid="signout">Sign out</button>}
        <Segmented<ThemeChoice> label="Theme" value={theme} onChange={onTheme}
          options={[
            { value: 'dark', label: 'Dark', icon: <MoonIcon /> },
            { value: 'light', label: 'Light', icon: <SunIcon /> },
            { value: 'auto', label: 'Auto', icon: <AutoIcon /> },
          ]} />
      </div>
    </header>
  )
}

export function OfflineBanner() {
  return (
    <div className="banner" role="status">
      <OfflineIcon /> <span><strong>You’re offline.</strong> Showing the data last saved on this device.</span>
    </div>
  )
}
