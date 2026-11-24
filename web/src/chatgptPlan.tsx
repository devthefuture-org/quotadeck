import { useEffect, useState } from 'preact/hooks'
import { desktopBrowserOpener, openExternally } from './externalBrowser'

type Status = {
  connected: boolean
  pending: boolean
  email?: string
  error?: string
  completed: number
  lastRequestId?: string
}

export type ChatGPTSelection = {
  available: boolean
  active: boolean
  model?: string
  fableModel?: string
  classifierModel?: string
  defaultClassifier?: string
  classifierPreference?: string
}

type PlanModel = { slug: string; display_name?: string }
const path = '/api/v1/control/chatgpt'

async function chatGPTRequest<T>(method = 'GET', signal?: AbortSignal, route = path): Promise<T> {
  const response = await fetch(route, { method, signal, headers: { 'X-QuotaDeck-Request': 'control' } })
  const payload = await response.json()
  if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`)
  return payload as T
}

export function ChatGPTPlan({ selection, planBusy, onUse }: {
  selection?: ChatGPTSelection; planBusy: string; onUse: (classifierModel?: string) => void
}) {
  const [status, setStatus] = useState<Status | null>(null)
  const [url, setURL] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [models, setModels] = useState<PlanModel[] | null>(null)
  const [modelError, setModelError] = useState('')
  const [draft, setDraft] = useState<string | null>(null)
  const { active, available, model, fableModel, classifierModel, classifierPreference = '', defaultClassifier: configuredClassifier } = selection ?? {}
  const alternateFable = active && fableModel !== model ? fableModel : undefined
  const connected = status?.connected === true
  const chosenClassifier = draft ?? classifierPreference
  const defaultClassifier = models?.find(item => item.slug === configuredClassifier) ?? models?.[0]
  const missingClassifier = models && chosenClassifier && !models.some(item => item.slug === chosenClassifier)

  useEffect(() => {
    const abort = new AbortController()
    let timer: number | undefined
    async function refresh() {
      try {
        setStatus(await chatGPTRequest<Status>('GET', abort.signal))
      } catch {
        if (!abort.signal.aborted) setStatus(null)
      } finally {
        if (!abort.signal.aborted) timer = window.setTimeout(() => void refresh(), 2_000)
      }
    }
    void refresh()
    return () => {
      window.clearTimeout(timer)
      abort.abort()
    }
  }, [])

  useEffect(() => {
    setModelError('')
    setModels(null)
    if (!connected) return
    const abort = new AbortController()
    void chatGPTRequest<PlanModel[]>('GET', abort.signal, path + '/models')
      .then(catalog => { if (!abort.signal.aborted) setModels(catalog) })
      .catch(err => { if (!abort.signal.aborted) setModelError(err instanceof Error ? err.message : 'Could not load ChatGPT models.') })
    return () => abort.abort()
  }, [connected])

  useEffect(() => {
    if (draft === classifierPreference) setDraft(null)
  }, [draft, classifierPreference])

  async function connect() {
    setBusy(true)
    setError('')
    try {
      const result = await chatGPTRequest<{ url: string }>('POST')
      setURL(result.url)
      setStatus(current => current && { ...current, pending: true, error: undefined })
      const openOnDesktop = desktopBrowserOpener()
      // Use the current tab in the web dashboard so popup blockers cannot
      // interfere. The desktop WebView opens the user's system browser.
      if (openOnDesktop) openOnDesktop(result.url)
      else window.location.assign(result.url)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'ChatGPT sign-in failed.')
    } finally {
      setBusy(false)
    }
  }

  async function disconnect() {
    setBusy(true)
    setError('')
    try {
      setStatus(await chatGPTRequest<Status>('DELETE', undefined, status?.pending ? path + '/login' : path))
      setURL('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'ChatGPT could not disconnect.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="plan-panel">
      <div class="plan-heading">
        <div><span class="provider-glyph codex">⌘</span><div><p>ChatGPT plan</p><small>OpenAI models in Claude Code</small></div></div>
        <span class={`availability ${status?.connected ? 'ok' : ''}`}>{active && status?.connected ? 'selected' : status?.connected ? 'connected' : status?.pending ? 'sign-in pending' : status ? 'setup needed' : 'unavailable'}</span>
      </div>
      <div class="plan-form">
        {status?.connected ? <>
          {status.email && <p>Connected as {status.email}</p>}
          {available
            ? <p>New Claude Code sessions, including in your editor, use the selected plan. Keep the QuotaDeck server running.</p>
            : <p>Start the QuotaDeck server to select ChatGPT for Claude Code.</p>}
          {alternateFable && <p>Choose <code>/model fable</code> in Claude Code for <code>{alternateFable}</code>.</p>}
          {status.completed > 0 && <p>OpenAI completed {status.completed} {status.completed === 1 ? 'request' : 'requests'} in this QuotaDeck server run.</p>}
          <label for="chatgpt-classifier">Auto mode classifier</label>
          <select id="chatgpt-classifier" disabled={!available || busy || planBusy !== '' || !models?.length}
            value={chosenClassifier} onChange={event => setDraft(event.currentTarget.value)}>
            <option value="">{defaultClassifier ? `Default (${defaultClassifier.display_name || defaultClassifier.slug})` : 'Default'}</option>
            {missingClassifier && <option value={chosenClassifier} disabled>{chosenClassifier} (unavailable)</option>}
            {models?.map(item => <option key={item.slug} value={item.slug}>{item.display_name || item.slug}</option>)}
          </select>
          {missingClassifier && classifierModel && <p>Using <code>{classifierModel}</code>.</p>}
          {modelError && <p role="alert">{modelError}</p>}
        </> : <p>Authorize QuotaDeck to use your ChatGPT plan, then select it for Claude Code.</p>}
        {error || status?.error ? <p role="alert">{error || status?.error}</p> : null}
        <div class="plan-actions">
          {status?.connected && <button class="refresh-button" disabled={!available || busy || planBusy !== ''} onClick={() => onUse(draft ?? undefined)}>
            {planBusy === 'chatgpt-activate' ? 'Applying…' : active ? (draft === null ? 'Reapply ChatGPT settings' : 'Apply classifier') : 'Use ChatGPT in Claude Code'}
          </button>}
          {status?.pending && url
            ? <a class="refresh-button" href={url} rel="noreferrer" onClick={event => openExternally(event, url)}>Open ChatGPT sign-in ↗</a>
            : <button class={status?.connected ? 'quiet-button' : 'refresh-button'} disabled={busy || !status || planBusy !== ''} onClick={() => void connect()}>
              {busy ? 'Working…' : status?.connected ? 'Reconnect ChatGPT' : 'Continue with ChatGPT'}
            </button>}
          {(status?.connected || status?.pending) && <button class="quiet-button" disabled={busy || planBusy !== ''} onClick={() => void disconnect()}>
            {status.pending ? 'Cancel sign-in' : 'Disconnect'}
          </button>}
        </div>
      </div>
    </section>
  )
}
