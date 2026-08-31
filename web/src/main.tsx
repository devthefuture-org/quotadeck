import { render } from 'preact'
import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import './styles.css'

type Provider = { id: string; name: string; enabled: boolean; source: string }
type Account = {
  id: string
  providerId: string
  label: string
  plan?: string
  active: boolean
  disabled?: boolean
  source: string
  sourceMeta?: Record<string, string>
}
type QuotaWindow = {
  id: string
  label: string
  kind: string
  scope?: string
  usedPercent?: number
  remainingPercent?: number
  used?: number
  limit?: number
  remaining?: number
  unit?: string
  resetsAt?: string
  expectedPercent?: number
  projectedExhaustionAt?: string
  willLastToReset?: boolean
}
type Snapshot = {
  fetchedAt: string
  sourceAgeSeconds?: number
  status: string
  stale: boolean
  errorCode?: string
  errorMessage?: string
  windows: QuotaWindow[]
}
type AccountState = { account: Account; snapshot: Snapshot }
type StateResponse = { generatedAt: string; providers: Provider[]; accounts: AccountState[] }
type ControlState = {
  mode: 'claude' | 'zai' | 'unknown'
  claude: { available: boolean; activeAccountId?: string }
  zai: { configured: boolean; active: boolean; endpoint: string }
}
type DoctorReport = {
  version: string
  generatedAt: string
  configPath: string
  databasePath: string
  tools: { name: string; present: boolean; path?: string; version?: string }[]
  sources: { provider: string; source: string; accepted: boolean; reason: string; metadata?: Record<string, string> }[]
}

const providerOrder: Record<string, number> = { claude: 0, codex: 1, zai: 2 }
const providerLabel: Record<string, string> = { claude: 'Claude', codex: 'Codex', zai: 'Z.ai' }

function App() {
  const [state, setState] = useState<StateResponse | null>(null)
  const [error, setError] = useState('')
  const [providerFilter, setProviderFilter] = useState('all')
  const [accountFilter, setAccountFilter] = useState('all')
  const [mode, setMode] = useState<'used' | 'remaining'>('used')
  const [refreshing, setRefreshing] = useState(false)
  const [view, setView] = useState<'dashboard' | 'plans' | 'diagnostics'>('dashboard')
  const [control, setControl] = useState<ControlState | null>(null)
  const [now, setNow] = useState(Date.now())

  const load = useCallback(async () => {
    try {
      const [stateResponse, controlResponse] = await Promise.all([
        fetch('/api/v1/state', { cache: 'no-store' }),
        fetch('/api/v1/control', { cache: 'no-store' }),
      ])
      if (!stateResponse.ok) throw new Error(`HTTP ${stateResponse.status}`)
      setState(await stateResponse.json() as StateResponse)
      setControl(controlResponse.ok ? await controlResponse.json() as ControlState : null)
      setError('')
    } catch {
      setError('QuotaDeck cannot reach its local API.')
    }
  }, [])

  useEffect(() => {
    void load()
    const events = new EventSource('/api/v1/events')
	const fallback = window.setInterval(() => void load(), 60_000)
    let pending = 0
    events.addEventListener('update', () => {
      window.clearTimeout(pending)
      pending = window.setTimeout(() => void load(), 150)
    })
    events.onerror = () => setError('Live updates paused; reconnecting…')
    return () => {
      window.clearTimeout(pending)
	  window.clearInterval(fallback)
      events.close()
    }
  }, [load])

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  const accounts = useMemo(() => {
    if (!state) return []
    return [...state.accounts]
      .filter(item => providerFilter === 'all' || item.account.providerId === providerFilter)
      .filter(item => accountFilter === 'all' || item.account.id === accountFilter)
      .sort((left, right) => {
        const byProvider = (providerOrder[left.account.providerId] ?? 99) - (providerOrder[right.account.providerId] ?? 99)
        return byProvider || left.account.label.localeCompare(right.account.label)
      })
  }, [state, providerFilter, accountFilter])

  const groups = useMemo(() => {
    const grouped = new Map<string, AccountState[]>()
    for (const item of accounts) {
      const values = grouped.get(item.account.providerId) ?? []
      values.push(item)
      grouped.set(item.account.providerId, values)
    }
    return grouped
  }, [accounts])

  async function refresh() {
    setRefreshing(true)
    try {
      const response = await fetch('/api/v1/refresh', {
        method: 'POST',
        headers: { 'X-QuotaDeck-Request': 'refresh' },
      })
      if (!response.ok && response.status !== 409) throw new Error(`HTTP ${response.status}`)
      await load()
    } catch {
      setError('The manual refresh could not complete.')
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div class="app-shell">
      <header class="topbar">
        <div class="brand-block">
          <div class="mark" aria-hidden="true"><span /><span /><span /></div>
          <div><p class="eyebrow">Local quota intelligence</p><h1>QuotaDeck</h1></div>
        </div>
        <div class="top-actions">
          <button class={`quiet-button ${view === 'plans' ? 'selected' : ''}`} onClick={() => setView(value => value === 'plans' ? 'dashboard' : 'plans')}>{view === 'plans' ? 'Dashboard' : 'Plans'}</button>
          <button class={`quiet-button ${view === 'diagnostics' ? 'selected' : ''}`} onClick={() => setView(value => value === 'diagnostics' ? 'dashboard' : 'diagnostics')}>{view === 'diagnostics' ? 'Dashboard' : 'Diagnostics'}</button>
          <button class="refresh-button" aria-label={refreshing ? 'Refreshing quotas' : 'Refresh quotas'} onClick={() => void refresh()} disabled={refreshing}>
            <span class={refreshing ? 'refresh-icon spinning' : 'refresh-icon'}>↻</span><span class="refresh-label">{refreshing ? 'Refreshing' : 'Refresh'}</span>
          </button>
        </div>
      </header>

      {view === 'diagnostics' ? <Diagnostics /> : view === 'plans' ? (
        <ControlCenter state={state} control={control} onChanged={setControl} />
      ) : (
        <main>
          <section class="hero">
            <div>
              <p class="kicker">Every window. Every account.</p>
              <h2>Know what you can ship<br />before the limit hits.</h2>
            </div>
            <div class="summary-card">
              <span class="live-dot" />
              <div><strong>{state?.accounts.length ?? 0} accounts</strong><small>Live from your machine</small></div>
            </div>
          </section>

          <section class="controls" aria-label="Dashboard filters">
            <div class="segmented">
              {['all', ...(state?.providers.map(item => item.id) ?? [])].map(id => (
                <button class={providerFilter === id ? 'active' : ''} onClick={() => { setProviderFilter(id); setAccountFilter('all') }}>
                  {id === 'all' ? 'All providers' : providerLabel[id] ?? id}
                </button>
              ))}
            </div>
            <div class="control-pair">
              <select aria-label="Filter by account" value={accountFilter} onChange={event => setAccountFilter(event.currentTarget.value)}>
                <option value="all">All accounts</option>
                {state?.accounts
                  .filter(item => providerFilter === 'all' || item.account.providerId === providerFilter)
                  .map(item => <option value={item.account.id}>{item.account.label}</option>)}
              </select>
              <button class="mode-button" onClick={() => setMode(value => value === 'used' ? 'remaining' : 'used')}>
                Showing {mode === 'used' ? 'consumed' : 'remaining'}
              </button>
            </div>
          </section>

          {error && <div class="notice" role="status">{error}</div>}

          <div class="provider-stack">
            {Array.from(groups.entries()).map(([provider, items]) => (
              <section class="provider-group" key={provider}>
                <div class="section-heading">
                  <span class={`provider-glyph ${provider}`}>{provider === 'claude' ? 'C' : provider === 'codex' ? '⌘' : 'Z'}</span>
                  <div><p>{providerLabel[provider] ?? provider}</p><span>{items.length} {items.length === 1 ? 'account' : 'accounts'}</span></div>
                </div>
                <div class="card-grid">
                  {items.map(item => <AccountCard
                    key={item.account.id}
                    state={item}
                    mode={mode}
                    now={now}
                    selected={control == null
                      ? item.account.active
                      : item.account.providerId === 'claude'
                        ? control.mode === 'claude' && control.claude.activeAccountId === item.account.id
                        : item.account.providerId === 'zai' && control.mode === 'zai'}
                  />)}
                </div>
              </section>
            ))}
          </div>

          {state && state.accounts.length === 0 && (
            <section class="empty-state">
              <span>◇</span><h3>No quota source detected yet</h3>
              <p>Open diagnostics to see which local tools, homes, and environment references QuotaDeck considered.</p>
              <button class="refresh-button" onClick={() => setView('diagnostics')}>Open diagnostics</button>
            </section>
          )}
        </main>
      )}

      <footer><span>QuotaDeck · local-first · zero telemetry</span><span>{state ? `Updated ${relativeTime(state.generatedAt, now)}` : 'Connecting…'}</span></footer>
    </div>
  )
}

function AccountCard({ state, mode, now, selected }: { state: AccountState; mode: 'used' | 'remaining'; now: number; selected: boolean }) {
  const { account, snapshot } = state
  const windows = [...(snapshot.windows ?? [])].sort((left, right) => {
    if (!left.resetsAt) return 1
    if (!right.resetsAt) return -1
    return new Date(left.resetsAt).getTime() - new Date(right.resetsAt).getTime()
  })
  const mostConstrained = windows.reduce<QuotaWindow | null>((selected, window) => {
    if (window.usedPercent == null) return selected
    return !selected || (selected.usedPercent ?? -1) < window.usedPercent ? window : selected
  }, null)
  return (
    <article class={`account-card status-${snapshot.status}`}>
      <div class="account-head">
        <div>
          <div class="badges">
            {account.plan && <span class="plan-badge">{account.plan}</span>}
            {selected && <span class="active-badge"><i /> selected</span>}
            {account.disabled && <span class="disabled-badge">disabled</span>}
          </div>
          <h3>{account.label}</h3>
          <p class="source-line">via {account.source}{account.sourceMeta?.slot ? ` · slot ${account.sourceMeta.slot}` : ''}</p>
        </div>
        <StatusBadge snapshot={snapshot} now={now} />
      </div>
      {snapshot.errorMessage && <div class="account-error">{snapshot.errorMessage}</div>}
      <div class="window-list">
        {windows.map(window => (
          <WindowRow key={window.id} window={window} mode={mode} now={now} constrained={window.id === mostConstrained?.id && windows.length > 1} />
        ))}
        {windows.length === 0 && <p class="no-windows">No actionable quota window is available.</p>}
      </div>
    </article>
  )
}

function ControlCenter({ state, control, onChanged }: { state: StateResponse | null; control: ControlState | null; onChanged: (next: ControlState) => void }) {
  const [apiKey, setAPIKey] = useState('')
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const apiKeyInput = useRef<HTMLInputElement>(null)
  const claudeAccounts = state?.accounts.filter(item => item.account.providerId === 'claude') ?? []
  const zaiSelected = control?.mode === 'zai'
  const zaiReady = Boolean(control?.zai.configured || apiKey.trim())

  async function request(path: string, method: string, body: unknown, busyKey: string) {
    setBusy(busyKey)
    setMessage('')
    setError('')
    try {
      const response = await fetch(path, {
        method,
        headers: { 'Content-Type': 'application/json', 'X-QuotaDeck-Request': 'control' },
        body: JSON.stringify(body),
      })
      const payload = await response.json().catch(() => null) as { refresh?: string; control?: ControlState; error?: { message?: string } } | null
      if (!response.ok) throw new Error(payload?.error?.message || `HTTP ${response.status}`)
      setAPIKey('')
      if (payload?.control) onChanged(payload.control)
      setMessage('Plan selection updated. New Claude Code sessions will use it.')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The plan selection could not be updated.')
    } finally {
      setBusy('')
    }
  }

  function selectClaude(accountId: string) {
    return request('/api/v1/control/claude/switch', 'POST', { accountId }, accountId)
  }

  function configureZAI(activate: boolean) {
    return request('/api/v1/control/zai', 'PUT', { apiKey, activate }, activate ? 'zai-activate' : 'zai-save')
  }

  function selectZAI() {
    if (zaiReady) return configureZAI(true)
    apiKeyInput.current?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    apiKeyInput.current?.focus({ preventScroll: true })
  }

  return (
    <main class="plan-control">
      <section class="hero compact control-hero">
        <div>
          <p class="kicker">Claude Code routing</p>
          <h2>Pick the plan for<br />your next session.</h2>
        </div>
        <div class="current-plan">
          <span>Selected now</span>
          <strong>{control?.mode === 'zai' ? 'Z.ai · GLM Coding Plan' : control?.mode === 'claude' ? 'Claude subscription' : 'No plan detected'}</strong>
          <small>Already-running Claude Code sessions keep their current environment.</small>
        </div>
      </section>

      {message && <div class="notice success" role="status">{message}</div>}
      {error && <div class="notice" role="alert">{error}</div>}

      <div class="plan-layout">
        <section class="plan-panel">
          <div class="plan-heading">
            <div><span class="provider-glyph claude">C</span><div><p>Claude Code plans</p><small>Claude subscriptions and compatible providers</small></div></div>
            <span class={`availability ${control?.claude.available || control?.zai.configured ? 'ok' : ''}`}>{control?.claude.available ? 'cswap ready' : control?.zai.configured ? 'Z.ai ready' : 'setup needed'}</span>
          </div>
          <div class="plan-options">
            {claudeAccounts.map(item => {
              const selected = control?.mode === 'claude' && control.claude.activeAccountId === item.account.id
              return <div class={`plan-option ${selected ? 'selected' : ''}`} key={item.account.id}>
                <div>
                  <span>{item.account.plan || 'Claude plan'}</span>
                  <strong>{item.account.label}</strong>
                  <small>cswap slot {item.account.sourceMeta?.slot ?? '—'}{item.account.disabled ? ' · disabled' : ''}</small>
                </div>
                <button
                  class={selected ? 'selected-plan-button' : 'plan-button'}
                  disabled={selected || item.account.disabled || busy !== '' || !control?.claude.available}
                  onClick={() => void selectClaude(item.account.id)}
                >{selected ? 'Selected' : busy === item.account.id ? 'Switching…' : 'Use plan'}</button>
              </div>
            })}
            <div class={`plan-option ${zaiSelected ? 'selected' : ''}`}>
              <div>
                <span>GLM Coding Plan</span>
                <strong>Z.ai</strong>
                <small>{control?.zai.configured ? 'API key stored · Anthropic-compatible' : 'Add an API key to enable'}</small>
              </div>
              <button
                class={zaiSelected ? 'selected-plan-button' : 'plan-button'}
                disabled={zaiSelected || busy !== ''}
                onClick={() => void selectZAI()}
              >{zaiSelected ? 'Selected' : busy === 'zai-activate' ? 'Switching…' : zaiReady ? 'Use plan' : 'Set up'}</button>
            </div>
            {claudeAccounts.length === 0 && <p class="control-empty">No cswap account is available; Z.ai can still be used independently.</p>}
          </div>
        </section>

        <section class={`plan-panel zai-panel ${control?.mode === 'zai' ? 'selected' : ''}`}>
          <div class="plan-heading">
            <div><span class="provider-glyph zai">Z</span><div><p>Z.ai</p><small>GLM Coding Plan for Claude Code</small></div></div>
            <span class={`availability ${control?.zai.configured ? 'ok' : ''}`}>{control?.zai.configured ? 'key stored' : 'setup needed'}</span>
          </div>
          <div class="zai-form">
            <label for="zai-api-key">Z.ai API key</label>
            <input
              id="zai-api-key"
              ref={apiKeyInput}
              type="password"
              value={apiKey}
              autoComplete="new-password"
              placeholder={control?.zai.configured ? 'Key already stored — leave blank to keep it' : 'Paste your Z.ai API key'}
              onInput={event => setAPIKey(event.currentTarget.value)}
            />
            <p>The key stays in private local files and is never returned to this page. QuotaDeck configures the official Anthropic endpoint <code>{control?.zai.endpoint ?? 'https://api.z.ai/api/anthropic'}</code>.</p>
            <div class="zai-actions">
              <button class="quiet-button" disabled={busy !== '' || apiKey.trim() === ''} onClick={() => void configureZAI(false)}>{busy === 'zai-save' ? 'Saving…' : 'Save key'}</button>
              <button class="refresh-button" disabled={busy !== '' || (!control?.zai.configured && apiKey.trim() === '') || (control?.mode === 'zai' && apiKey.trim() === '')} onClick={() => void configureZAI(true)}>
                {busy === 'zai-activate' ? 'Configuring…' : control?.mode === 'zai' ? (apiKey.trim() === '' ? 'Selected' : 'Update active key') : 'Save & use Z.ai'}
              </button>
            </div>
            <a href="https://z.ai/manage-apikey/apikey-list" target="_blank" rel="noreferrer">Open Z.ai API key management ↗</a>
          </div>
        </section>
      </div>
      <p class="control-note">Switching affects new Claude Code processes. QuotaDeck preserves unrelated Claude settings and never opens the cswap credential store.</p>
    </main>
  )
}

function WindowRow({ window, mode, now, constrained }: { window: QuotaWindow; mode: 'used' | 'remaining'; now: number; constrained: boolean }) {
  const rawUsed = window.usedPercent ?? percentageFromValues(window)
  const used = rawUsed == null ? null : clamp(rawUsed)
  const percentage = used == null ? null : mode === 'used' ? used : 100 - used
  const reset = window.resetsAt ? relativeTime(window.resetsAt, now) : 'No reset supplied'
  const title = window.resetsAt ? new Date(window.resetsAt).toLocaleString() : undefined
  return (
    <div class={constrained ? 'window-row constrained' : 'window-row'}>
      <div class="window-title">
        <div><strong>{window.label}</strong>{window.scope && <span>{window.scope}</span>}</div>
        {constrained && <em>tightest</em>}
      </div>
      <div class="meter-line">
        <div class="meter" role="progressbar" aria-valuenow={percentage == null ? undefined : Math.round(percentage)} aria-valuemin={0} aria-valuemax={100}>
          <span style={{ width: `${percentage ?? 0}%` }} />
        </div>
        <strong>{percentage == null ? '—' : `${Math.round(percentage)}%`}</strong>
      </div>
      <div class="window-meta">
        <span>{mode === 'used' ? 'consumed' : 'remaining'}</span>
        <span title={title}>{reset}</span>
      </div>
      {window.expectedPercent != null && <div class="pace">Expected now: {Math.round(window.expectedPercent)}%{window.willLastToReset === false ? ' · projected to exhaust early' : ''}</div>}
    </div>
  )
}

function StatusBadge({ snapshot, now }: { snapshot: Snapshot; now: number }) {
  const label = snapshot.status.replace('_', ' ')
  const age = snapshot.sourceAgeSeconds != null
    ? compactDuration(snapshot.sourceAgeSeconds * 1000)
    : relativeTime(snapshot.fetchedAt, now)
  return <div class={`status-badge ${snapshot.status}`}><i /> <span>{label}<small>{age}</small></span></div>
}

function Diagnostics() {
  const [report, setReport] = useState<DoctorReport | null>(null)
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    void fetch('/api/v1/doctor', { cache: 'no-store' }).then(response => response.json()).then(setReport)
  }, [])
  async function copy() {
    if (!report) return
    await navigator.clipboard.writeText(JSON.stringify(report, null, 2))
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1800)
  }
  return (
    <main class="diagnostics">
      <section class="hero compact"><div><p class="kicker">Explainable discovery</p><h2>Diagnostics, without secrets.</h2></div></section>
      {!report ? <p>Inspecting local metadata…</p> : (
        <>
          <div class="diagnostic-summary">
            <div><span>Version</span><strong>{report.version}</strong></div>
            <div><span>Config</span><strong>{report.configPath}</strong></div>
            <div><span>Database</span><strong>{report.databasePath}</strong></div>
          </div>
          <section class="diagnostic-section"><h3>Local tools</h3><div class="diagnostic-list">
            {report.tools.map(tool => <div key={tool.name}><i class={tool.present ? 'ok' : ''} /><strong>{tool.name}</strong><span>{tool.present ? tool.version || tool.path : 'not found'}</span></div>)}
          </div></section>
          <section class="diagnostic-section"><h3>Discovery decisions</h3><div class="source-table">
            {report.sources.map((source, index) => <div class="source-row" key={`${source.provider}-${source.source}-${index}`}><span class={`decision ${source.accepted ? 'accepted' : 'rejected'}`}>{source.accepted ? 'accepted' : 'rejected'}</span><strong>{providerLabel[source.provider] ?? source.provider} · {source.source}</strong><p>{source.reason}</p></div>)}
          </div></section>
          <button class="refresh-button" onClick={() => void copy()}>{copied ? 'Copied' : 'Copy redacted JSON'}</button>
        </>
      )}
    </main>
  )
}

function percentageFromValues(window: QuotaWindow): number | null {
  if (window.used != null && window.limit) return window.used / window.limit * 100
  if (window.remaining != null && window.limit) return 100 - window.remaining / window.limit * 100
  return null
}

function clamp(value: number): number { return Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0)) }

function relativeTime(raw: string, now: number): string {
  const delta = new Date(raw).getTime() - now
  if (!Number.isFinite(delta)) return 'unknown'
  const absolute = Math.abs(delta)
  if (absolute < 5000) return delta >= 0 ? 'now' : 'just now'
  const text = compactDuration(absolute)
  return delta >= 0 ? `in ${text}` : `${text} ago`
}

function compactDuration(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h ${minutes % 60}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

render(<App />, document.getElementById('app')!)
