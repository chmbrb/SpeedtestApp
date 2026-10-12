import { useCallback, useEffect, useState } from 'react'

export type ThemeChoice = 'dark' | 'light' | 'auto'

const read = (): ThemeChoice => {
  try {
    const v = localStorage.getItem('theme')
    return v === 'light' || v === 'auto' ? v : 'dark' // dark is the default
  } catch {
    return 'dark'
  }
}

const resolve = (c: ThemeChoice): 'dark' | 'light' =>
  c === 'auto' ? (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark') : c

export function useTheme(): [ThemeChoice, (c: ThemeChoice) => void] {
  const [choice, setChoice] = useState<ThemeChoice>(read)

  useEffect(() => {
    const apply = () => {
      document.documentElement.dataset.theme = resolve(choice)
      const meta = document.querySelector('meta[name="theme-color"]')
      meta?.setAttribute('content', resolve(choice) === 'dark' ? '#111821' : '#f7f8f9')
    }
    apply()
    if (choice !== 'auto') return
    const mq = matchMedia('(prefers-color-scheme: light)')
    mq.addEventListener('change', apply)
    return () => mq.removeEventListener('change', apply)
  }, [choice])

  const set = useCallback((c: ThemeChoice) => {
    setChoice(c)
    try {
      localStorage.setItem('theme', c)
    } catch {
      /* private mode: the choice still applies for this session */
    }
  }, [])

  return [choice, set]
}
