import { useCallback, useEffect, useRef, useState } from 'preact/hooks'
import { acceptSnapshot, formatRemaining, isActive, signInView } from './codexLogin.js'

type LoginSnapshot = {
  loginId: string
  accountId?: string
  mode?: 'browser' | 'deviceCode'
  state: string
  authUrl?: string
  verificationURL?: string
  verificationUrl?: string
  userCode?: string
  error?: string
  startedAt?: string
  expiresAt?: string
}

async function control(path: string, init: RequestInit): Promise<any> {
  const response = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', 'X-QuotaDeck-Request': 'control', ...(init.headers ?? {}) },
  })
  const payload = await response.json().catch(() => null)
  if (!response.ok) throw new Error(payload?.error?.message || `HTTP ${response.status}`)
  return payload
}

/**
 * Tracks one account's sign-in.
 *
 * The event stream is only a hint that something changed: the hub drops events
 * when a client falls behind, and a page reload forgets the sign-in id. The
 * authoritative answer is always re-read from the server, keyed by account.
 */
export function useCodexSignIn(accountId: string, enabled: boolean, onFinished: () => void) {
  const [login, setLogin] = useState<LoginSnapshot | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const current = useRef<LoginSnapshot | null>(null)
  const finished = useRef(onFinished)
  finished.current = onFinished

  const apply = useCallback((incoming: LoginSnapshot | null) => {
    if (!acceptSnapshot(current.current, incoming)) return
    const previous = current.current
    current.current = incoming && incoming.state !== 'none' ? incoming : null
    setLogin(current.current)
    if (previous && isActive(previous) && incoming?.state === 'completed') finished.current()
  }, [])

  const reconcile = useCallback(async () => {
    if (!enabled) return
    try {
      apply(await control(`/api/v1/control/codex/login?accountId=${encodeURIComponent(accountId)}`, { method: 'GET' }))
    } catch {
      // Reconciliation is a repair path; a failure here is retried on the next tick.
    }
  }, [accountId, enabled, apply])

  useEffect(() => {
    void reconcile()
  }, [reconcile])

  // Poll while a sign-in is running: the stream may have dropped the very event
  // that announces the result, and nothing else would then clear the card.
  useEffect(() => {
    if (!isActive(login)) return
    const timer = window.setInterval(() => void reconcile(), 4000)
    return () => window.clearInterval(timer)
  }, [login, reconcile])

  const start = useCallback(async (mode: 'browser' | 'deviceCode') => {
    setBusy(true)
    setError('')
    try {
      apply(await control('/api/v1/control/codex/login', {
        method: 'POST',
        body: JSON.stringify({ accountId, mode }),
      }))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'The sign-in could not be started.')
    } finally {
      setBusy(false)
    }
  }, [accountId, apply])

  const cancel = useCallback(async (loginId: string) => {
    setBusy(true)
    try {
      await control(`/api/v1/control/codex/login/${encodeURIComponent(loginId)}`, { method: 'DELETE' })
    } catch {
      // Whether or not the daemon still knows this sign-in, re-reading tells us.
    } finally {
      setBusy(false)
      void reconcile()
    }
  }, [reconcile])

  return { login, busy, error, start, cancel, reconcile }
}

export function CodexSignIn({ state, now, onFinished }: {
  state: { account: { id: string }; snapshot: { status: string; errorCode?: string } }
  now: number
  onFinished: () => void
}) {
  const accountSnapshot = state.snapshot
  const offered = accountSnapshot.status === 'auth_error' || accountSnapshot.errorCode === 'codex_auth_required'
  const signIn = useCodexSignIn(state.account.id, offered, onFinished)
  const view = signInView(accountSnapshot, signIn.login, now)
  if (view.kind === 'none') return null

  return (
    <div class="account-signin">
      {view.kind === 'offer' && <>
        <button class="plan-button" disabled={signIn.busy} onClick={() => void signIn.start('browser')}>
          {signIn.busy ? 'Starting…' : 'Reconnect'}
        </button>
        <button class="quiet-button" disabled={signIn.busy} onClick={() => void signIn.start('deviceCode')}>
          Use a code instead
        </button>
        <small>Signing in here can interrupt a `codex login` running in a terminal.</small>
      </>}

      {view.kind === 'browser' && <>
        {/* A real link, not window.open: the URL arrives after an await, so a
            programmatic popup would be outside the user gesture and blocked. */}
        <a class="plan-button" href={view.authUrl} target="_blank" rel="noreferrer"
          onClick={event => openExternally(event, view.authUrl)}>
          Open the ChatGPT sign-in
        </a>
        <small>Waiting for you to finish in the browser · {formatRemaining(view.remainingMs)} left</small>
        <button class="quiet-button" disabled={signIn.busy}
          onClick={() => void signIn.cancel(view.loginId)}>Cancel</button>
      </>}

      {view.kind === 'deviceCode' && <>
        <p class="device-code">{view.userCode}</p>
        <a class="plan-button" href={view.verificationUrl} target="_blank" rel="noreferrer"
          onClick={event => openExternally(event, view.verificationUrl)}>
          Open the activation page
        </a>
        <small>Enter the code above · {formatRemaining(view.remainingMs)} left</small>
        <button class="quiet-button" disabled={signIn.busy}
          onClick={() => void signIn.cancel(view.loginId)}>Cancel</button>
      </>}

      {view.kind === 'completed' && <small class="signin-ok">Signed in. Refreshing quotas…</small>}

      {view.kind === 'retry' && <>
        <small class="signin-error">{view.message}</small>
        <button class="plan-button" disabled={signIn.busy} onClick={() => void signIn.start('browser')}>
          Try again
        </button>
        <button class="quiet-button" disabled={signIn.busy} onClick={() => void signIn.start('deviceCode')}>
          Use a code instead
        </button>
      </>}

      {signIn.error && <small class="signin-error">{signIn.error}</small>}
    </div>
  )
}

type WailsWindow = Window & { runtime?: { BrowserOpenURL?: (url: string) => void } }

// The desktop build serves this same UI inside a WebView, where target="_blank"
// does not necessarily reach the system browser. Wails exposes the explicit
// call; in a plain browser tab the anchor already does the right thing.
function openExternally(event: Event, url: string) {
  const open = (window as WailsWindow).runtime?.BrowserOpenURL
  if (!open || !url) return
  event.preventDefault()
  open(url)
}
