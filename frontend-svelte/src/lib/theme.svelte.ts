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

class Theme {
  choice = $state<ThemeChoice>(read())

  constructor() {
    this.#apply()
    matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { if (this.choice === 'auto') this.#apply() })
  }

  #apply() {
    document.documentElement.dataset.theme = resolve(this.choice)
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', resolve(this.choice) === 'dark' ? '#111821' : '#f7f8f9')
  }

  set = (c: ThemeChoice) => {
    this.choice = c
    this.#apply()
    try {
      localStorage.setItem('theme', c)
    } catch {
      /* private mode: the choice still applies for this session */
    }
  }
}

export const theme = new Theme()
