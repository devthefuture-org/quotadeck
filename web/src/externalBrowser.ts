type WailsWindow = Window & { runtime?: { BrowserOpenURL?: (url: string) => void } }

export function desktopBrowserOpener() {
  const runtime = (window as WailsWindow).runtime
  return runtime?.BrowserOpenURL?.bind(runtime)
}

export function openExternally(event: Event, url: string) {
  const open = desktopBrowserOpener()
  if (!open || !url) return
  event.preventDefault()
  open(url)
}
