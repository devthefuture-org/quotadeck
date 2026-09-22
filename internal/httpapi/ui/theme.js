// Run before styles load to restore the theme without a flash of the system palette.
// Keep the storage key and accepted values in sync with src/theme.ts.
(() => {
  let preference = 'system'
  try {
    const saved = localStorage.getItem('quotadeck.theme')
    if (saved === 'light' || saved === 'dark') preference = saved
  } catch {
    // The theme still works when browser storage is unavailable.
  }
  document.documentElement.dataset.theme = preference === 'system'
    ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : preference
})()
