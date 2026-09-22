import { useLayoutEffect, useState } from 'preact/hooks'

type ThemePreference = 'system' | 'light' | 'dark'
// Also used by public/theme.js, which restores the palette before the app loads.
const storageKey = 'quotadeck.theme'

function readPreference(): ThemePreference {
  try {
    const saved = localStorage.getItem(storageKey)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // Storage may be disabled; keep theme selection available for this session.
  }
  return 'system'
}

export function useTheme() {
  const [theme, setTheme] = useState<ThemePreference>(readPreference)

  useLayoutEffect(() => {
    const systemTheme = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => {
      const resolved = theme === 'system' ? (systemTheme.matches ? 'dark' : 'light') : theme
      document.documentElement.dataset.theme = resolved
      document.querySelector('meta[name="theme-color"]')?.setAttribute('content',
        getComputedStyle(document.documentElement).getPropertyValue('--page-bg').trim())
    }
    apply()
    systemTheme.addEventListener('change', apply)
    return () => systemTheme.removeEventListener('change', apply)
  }, [theme])

  useLayoutEffect(() => {
    const sync = (event: StorageEvent) => {
      if (event.key === storageKey || event.key === null) setTheme(readPreference())
    }
    window.addEventListener('storage', sync)
    return () => window.removeEventListener('storage', sync)
  }, [])

  function selectTheme(next: ThemePreference) {
    setTheme(next)
    try {
      localStorage.setItem(storageKey, next)
    } catch {
      // Applying a theme does not depend on persisting it.
    }
  }

  return { theme, selectTheme }
}
