import { render } from 'preact'
import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { useTheme } from './theme'
import { CodexSignIn } from './codexSignIn'
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
  mode: 'claude' | 'zai' | 'kimi' | 'opencode' | 'unknown'
  claude: { available: boolean; activeAccountId?: string }
  zai: { configured: boolean; active: boolean; endpoint: string }
  opencode: { configured: boolean; active: boolean; endpoint: string; model?: string; source?: string; localAvailable?: boolean }
  kimi: { configured: boolean; active: boolean; endpoint: string; model?: string; source?: string; localAvailable?: boolean }
}
type DoctorReport = {
  version: string
  generatedAt: string
  configPath: string
  databasePath: string
  tools: { name: string; present: boolean; path?: string; version?: string }[]
  sources: { provider: string; source: string; accepted: boolean; reason: string; metadata?: Record<string, string> }[]
}

const providerOrder: Record<string, number> = { claude: 0, zai: 1, codex: 2, kimi: 3, opencode: 4 }
const providerLabel: Record<string, string> = { claude: 'Claude', codex: 'Codex', zai: 'Z.ai', kimi: 'Kimi', opencode: 'OpenCode Go' }
type APIPlan = 'zai' | 'kimi'
type SelectablePlan = APIPlan | 'opencode'
const apiPlans = {
  opencode: { label: 'OpenCode Go', glyph: 'O', description: 'DeepSeek V4.1 Flash for Claude Code' },
  zai: { label: 'Z.ai', glyph: 'Z', description: 'GLM Coding Plan for Claude Code', endpoint: 'https://api.z.ai/api/anthropic', keyURL: 'https://z.ai/manage-apikey/apikey-list' },
  kimi: { label: 'Kimi', glyph: '☾', description: 'Kimi K3 for Claude Code', endpoint: 'https://api.kimi.com/coding/', keyURL: 'https://www.kimi.com/code/console' },
}

function isPlanSelected(control: ControlState | null, provider: SelectablePlan) {
  return control?.mode === provider && (provider !== 'kimi' || control.kimi?.model === 'k3') && (provider !== 'opencode' || control.opencode?.model === 'deepseek-v4.1-flash')
}

function App() {
  const { theme, selectTheme } = useTheme()
  const [state, setState] = useState<StateResponse | null>(null)
  const [error, setError] = useState('')
  const [providerFilter, setProviderFilter] = useState('all')
  const [accountFilter, setAccountFilter] = useState('all')
  const [mode, setMode] = useState<'used' | 'remaining'>('used')
  const [refreshing, setRefreshing] = useState(false)
  const [view, setView] = useState<'dashboard' | 'diagnostics'>('dashboard')
  const [control, setControl] = useState<ControlState | null>(null)
  const [setupOpen, setSetupOpen] = useState(false)
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

  const plans = usePlanControl(async next => {
    setControl(next)
    await load()
  })
  const selectedAccount = state?.accounts.find(item => item.account.id === control?.claude.activeAccountId)?.account
  const selectedPlan = !control ? 'Plan selection unavailable'
    : control.mode === 'zai' ? 'Z.ai · GLM Coding Plan'
    : control.mode === 'opencode' ? 'OpenCode Go · DeepSeek V4.1 Flash'
    : control.mode === 'kimi' ? `Kimi · ${control.kimi?.model === 'k3' ? 'K3' : control.kimi?.model || 'Coding Plan'}`
    : control.mode === 'claude' ? [selectedAccount?.label || 'Claude subscription', selectedAccount?.plan].filter(Boolean).join(' · ')
    : 'No plan selected'

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
          <select class="theme-select" aria-label="Color theme" value={theme} onChange={event => {
            const next = event.currentTarget.value
            if (next === 'system' || next === 'light' || next === 'dark') selectTheme(next)
          }}>
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
          <button class={`quiet-button ${view === 'diagnostics' ? 'selected' : ''}`} onClick={() => setView(value => value === 'diagnostics' ? 'dashboard' : 'diagnostics')}>{view === 'diagnostics' ? 'Dashboard' : 'Diagnostics'}</button>
          <button class="refresh-button" aria-label={refreshing ? 'Refreshing quotas' : 'Refresh quotas'} onClick={() => void refresh()} disabled={refreshing}>
            <span class={refreshing ? 'refresh-icon spinning' : 'refresh-icon'}>↻</span><span class="refresh-label">{refreshing ? 'Refreshing' : 'Refresh'}</span>
          </button>
        </div>
      </header>

      {view === 'diagnostics' ? <Diagnostics /> : (
        <main>
          <section class="hero dashboard-hero">
            <div>
              <p class="kicker">Usage & plan selection</p>
              <h2>Your next session,<br />with room to ship.</h2>
              <p class="hero-description">Compare usage, then choose a plan for Claude Code.</p>
            </div>
            <div class="current-plan" aria-label="Selected Claude Code plan">
              <span>Claude Code · selected plan</span>
              <strong>{state ? selectedPlan : 'Loading plans…'}</strong>
              <small>Changes apply to new sessions. Running sessions keep their current plan.</small>
              <button class="quiet-button" aria-expanded={setupOpen} aria-controls="plan-setup" onClick={() => setSetupOpen(value => !value)}>
                {setupOpen ? 'Close setup' : 'Manage plans'}
              </button>
            </div>
          </section>

          <div id="plan-setup" hidden={!setupOpen}>
            <PlanSetup state={state} control={control} plans={plans} />
          </div>

          <section class="controls" aria-label="Dashboard filters">
            <div class="segmented">
              {['all', ...(state?.providers.map(item => item.id) ?? [])].map(id => (
                <button key={id} class={providerFilter === id ? 'active' : ''} aria-pressed={providerFilter === id} onClick={() => { setProviderFilter(id); setAccountFilter('all') }}>
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
          {state && !control && <div class="notice" role="status">Plan controls are unavailable. You can still view quota usage.</div>}
          {plans.message && <div class="notice success" role="status">{plans.message}</div>}
          {plans.error && <div class="notice" role="alert">{plans.error}</div>}

          <div class="provider-stack">
            {Array.from(groups.entries()).map(([provider, items]) => (
              <section class="provider-group" key={provider}>
                <div class="section-heading">
                  <span class={`provider-glyph ${provider}`}>{provider === 'claude' ? 'C' : provider === 'codex' ? '⌘' : provider === 'kimi' ? '☾' : provider === 'opencode' ? 'O' : 'Z'}</span>
                  <div><p>{provider === 'claude' ? 'Claude Code' : providerLabel[provider] ?? provider}</p><span>{items.length} {items.length === 1 ? 'account' : 'accounts'}</span></div>
                </div>
                <div class="provider-content">
                  {(provider === 'zai' || provider === 'kimi' || provider === 'opencode') && <div class="provider-plan-action">
                    <div><strong>{apiPlans[provider].description}</strong><small>{isPlanSelected(control, provider) ? 'Selected for new sessions' : provider === 'opencode' ? 'Use your local OpenCode connection for new sessions' : provider === 'kimi' && control?.kimi?.source === 'kimi-cli' ? 'Use your local Kimi Code login for new sessions' : `Use your configured ${apiPlans[provider].label} key for new sessions`}</small></div>
                    <button class={isPlanSelected(control, provider) ? 'selected-plan-button' : 'plan-button'}
                      disabled={!control || isPlanSelected(control, provider) || plans.busy !== ''}
                      onClick={() => {
                        if (control?.[provider]?.configured) void plans.selectAPIPlan(provider)
                        else {
                          setSetupOpen(true)
                          requestAnimationFrame(() => provider !== 'opencode' && plans.apiKeyInputs[provider].current?.focus())
                        }
                      }}>
                      {plans.busy === `${provider}-activate` ? 'Switching…' : isPlanSelected(control, provider) ? 'Selected in Claude Code' : control?.[provider]?.configured ? `Use ${apiPlans[provider].label}${provider === 'kimi' ? ' K3' : ''} in Claude Code` : `Set up ${apiPlans[provider].label}`}
                    </button>
                  </div>}
                  <div class="card-grid">
                    {items.map(item => <AccountCard
                      key={item.account.id}
                      state={item}
                      mode={mode}
                      now={now}
                      selected={item.account.providerId === 'claude'
                        ? control?.mode === 'claude' && control.claude.activeAccountId === item.account.id
                        : item.account.providerId === 'codex' && item.account.active}
                      planAction={item.account.providerId === 'claude' ? {
                        busy: plans.busy === item.account.id,
                        disabled: Boolean(item.account.disabled || plans.busy !== '' || !control?.claude.available),
                        unavailable: !control?.claude.available,
                        onSelect: () => void plans.selectClaude(item.account.id),
                      } : undefined}
                      onSignedIn={() => void load()}
                    />)}
                  </div>
                </div>
              </section>
            ))}
          </div>

          {state && state.accounts.length === 0 && (
            <section class="empty-state">
              <span>◇</span><h3>No quota source detected yet</h3>
              <p>Use Manage plans to set up Claude, Z.ai, Kimi or OpenCode Go, or open diagnostics to inspect available quota sources.</p>
              <button class="refresh-button" onClick={() => setView('diagnostics')}>Open diagnostics</button>
            </section>
          )}
        </main>
      )}

      <footer><span>QuotaDeck · local-first · zero telemetry</span><span>{state ? `Updated ${relativeTime(state.generatedAt, now)}` : 'Connecting…'}</span></footer>
    </div>
  )
}

type PlanAction = { busy: boolean; disabled: boolean; unavailable: boolean; onSelect: () => void }

function AccountCard({ state, mode, now, selected, planAction, onSignedIn }: { state: AccountState; mode: 'used' | 'remaining'; now: number; selected: boolean; planAction?: PlanAction; onSignedIn?: () => void }) {
  const { account, snapshot } = state
  const windows = snapshot.windows ?? []
  const mostConstrained = windows.reduce<QuotaWindow | null>((selected, window) => {
    if (window.usedPercent == null) return selected
    return !selected || (selected.usedPercent ?? -1) < window.usedPercent ? window : selected
  }, null)
  return (
    <article class={`account-card status-${snapshot.status} ${selected && planAction ? 'selected' : ''}`} aria-label={`${account.label}${account.plan ? ` · ${account.plan}` : ''}`}>
      <div class="account-head">
        <div>
          <div class="badges">
            {account.plan && <span class="plan-badge">{account.plan}</span>}
            {selected && <span class="active-badge"><i /> {planAction ? 'Claude Code · selected' : 'active'}</span>}
            {account.disabled && <span class="disabled-badge">disabled</span>}
          </div>
          <h3>{account.label}</h3>
          <p class="source-line">via {account.source}{account.sourceMeta?.slot ? ` · slot ${account.sourceMeta.slot}` : ''}</p>
        </div>
        <StatusBadge snapshot={snapshot} now={now} />
      </div>
      {snapshot.errorMessage && <div class="account-error">{snapshot.errorMessage}</div>}
      {account.providerId === 'codex' && <CodexSignIn state={state} now={now} onFinished={onSignedIn ?? (() => {})} />}
      <div class="window-list">
        {windows.map(window => (
          <WindowRow key={window.id} window={window} mode={mode} now={now} constrained={window.id === mostConstrained?.id && windows.length > 1} />
        ))}
        {windows.length === 0 && <p class="no-windows">No actionable quota window is available.</p>}
      </div>
      {planAction && <div class="account-plan-action">
        <button class={selected ? 'selected-plan-button' : 'plan-button'}
          disabled={selected || planAction.disabled}
          aria-label={selected ? `${account.label} is selected in Claude Code` : `Use ${account.label} in Claude Code`}
          onClick={planAction.onSelect}>
          {planAction.busy ? 'Switching…' : selected ? 'Selected in Claude Code' : account.disabled ? 'Account disabled' : 'Use in Claude Code'}
        </button>
        {planAction.unavailable && !account.disabled && <small>Plan selection unavailable. Check Manage plans.</small>}
      </div>}
    </article>
  )
}

function usePlanControl(onChanged: (next: ControlState) => Promise<void>) {
  const [apiKeys, setAPIKeys] = useState({ zai: '', kimi: '' })
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const apiKeyInputs = { zai: useRef<HTMLInputElement>(null), kimi: useRef<HTMLInputElement>(null) }
  const inFlight = useRef(false)

  async function request(path: string, method: string, body: unknown, busyKey: string, successMessage = 'Plan selection updated. New Claude Code sessions will use it.') {
    if (inFlight.current) return
    inFlight.current = true
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
      if (busyKey.startsWith('zai-')) setAPIKeys(keys => ({ ...keys, zai: '' }))
      if (busyKey.startsWith('kimi-')) setAPIKeys(keys => ({ ...keys, kimi: '' }))
      if (payload?.control) await onChanged(payload.control)
      setMessage(successMessage)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The plan selection could not be updated.')
    } finally {
      inFlight.current = false
      setBusy('')
    }
  }

  function selectClaude(accountId: string) {
    return request('/api/v1/control/claude/switch', 'POST', { accountId }, accountId)
  }

  async function setupClaude() {
    if (inFlight.current) return
    inFlight.current = true
    setBusy('cswap-setup')
    setMessage('')
    setError('')
    try {
      const response = await fetch('/api/v1/control/claude/setup', {
        method: 'POST',
        headers: { 'X-QuotaDeck-Request': 'control' },
      })
      const payload = await response.json().catch(() => null) as {
        control?: ControlState
        setup?: { installed: boolean; accountAdded: boolean; accountCount: number }
        error?: { message?: string }
      } | null
      if (!response.ok) throw new Error(payload?.error?.message || `HTTP ${response.status}`)
      if (payload?.control) await onChanged(payload.control)
      const count = payload?.setup?.accountCount ?? 0
      setMessage(`cswap is ready with ${count} Claude account${count === 1 ? '' : 's'}.`)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'cswap setup could not complete.')
    } finally {
      inFlight.current = false
      setBusy('')
    }
  }

  function setAPIKey(provider: APIPlan, value: string) {
    setAPIKeys(keys => ({ ...keys, [provider]: value }))
  }

  function configureAPIPlan(provider: APIPlan, activate: boolean) {
    return request(`/api/v1/control/${provider}`, 'PUT', { apiKey: apiKeys[provider], activate }, `${provider}-${activate ? 'activate' : 'save'}`,
      activate ? 'Plan selection updated. New Claude Code sessions will use it.' : `${apiPlans[provider].label} API key saved.`)
  }

  function selectAPIPlan(provider: SelectablePlan) {
    return request(`/api/v1/control/${provider}`, 'PUT', provider === 'opencode' ? { activate: true } : { apiKey: '', activate: true }, `${provider}-activate`)
  }

  return { apiKeys, setAPIKey, apiKeyInputs, busy, message, error, selectClaude, setupClaude, configureAPIPlan, selectAPIPlan }
}

function PlanSetup({ state, control, plans }: { state: StateResponse | null; control: ControlState | null; plans: ReturnType<typeof usePlanControl> }) {
  const { apiKeys, setAPIKey, apiKeyInputs, busy, setupClaude, configureAPIPlan } = plans
  const claudeAccounts = state?.accounts.filter(item => item.account.providerId === 'claude') ?? []

  return (
    <section class="plan-layout" aria-label="Plan setup">
      <section class="plan-panel">
        <div class="plan-heading">
          <div><span class="provider-glyph claude">C</span><div><p>Claude subscriptions</p><small>Accounts managed by cswap</small></div></div>
          <span class={`availability ${control?.claude.available ? 'ok' : ''}`}>{!control ? 'unavailable' : control.claude.available ? 'cswap ready' : 'setup needed'}</span>
        </div>
        {control && (!control.claude.available || claudeAccounts.length === 0) ? <div class="plan-option setup-option">
          <div>
            <strong>{control.claude.available ? 'Add your current Claude login' : 'Install and configure cswap'}</strong>
            <small>Sign in to Claude Code first. Setup imports your current login so you can view its usage and select it here.</small>
          </div>
          <button class="plan-button" disabled={busy !== ''} onClick={() => void setupClaude()}>
            {busy === 'cswap-setup' ? 'Setting up…' : control.claude.available ? 'Set up cswap' : 'Install & set up'}
          </button>
        </div> : <p class="control-empty">{control ? 'Your accounts are ready. Choose Use in Claude Code on a quota card below.' : 'Plan controls are temporarily unavailable.'}</p>}
      </section>
      <section class="plan-panel">
        <div class="plan-heading">
          <div><span class="provider-glyph opencode">O</span><div><p>OpenCode Go</p><small>DeepSeek V4.1 Flash for Claude Code</small></div></div>
          <span class={`availability ${control?.opencode?.configured ? 'ok' : ''}`}>{control?.opencode?.configured ? 'local login' : 'setup needed'}</span>
        </div>
        <div class="plan-form">
          <p>{control?.opencode?.configured ? 'Your OpenCode v2 connection is available. QuotaDeck renews it automatically and tracks your Go usage.' : 'Connect OpenCode Console in OpenCode v2 to see your Go quotas and use DeepSeek in Claude Code.'}</p>
          <p>Uses the DeepSeek V4.1 Flash model available through Go. Enable Global in your OpenCode workspace privacy settings to allow this model.</p>
          <button class="refresh-button" disabled={!control?.opencode?.configured || busy !== '' || isPlanSelected(control, 'opencode')} onClick={() => void plans.selectAPIPlan('opencode')}>
            {busy === 'opencode-activate' ? 'Switching…' : isPlanSelected(control, 'opencode') ? 'Selected in Claude Code' : 'Use DeepSeek V4.1 Flash'}
          </button>
          <a href="https://opencode.ai/console" target="_blank" rel="noreferrer">Open OpenCode Console ↗</a>
        </div>
      </section>
      {(['zai', 'kimi'] as const).map(provider => <section class="plan-panel" key={provider}>
        <div class="plan-heading">
          <div><span class={`provider-glyph ${provider}`}>{apiPlans[provider].glyph}</span><div><p>{apiPlans[provider].label}</p><small>{apiPlans[provider].description}</small></div></div>
          <span class={`availability ${control?.[provider]?.configured ? 'ok' : ''}`}>{!control ? 'unavailable' : provider === 'kimi' && control.kimi?.source === 'kimi-cli' ? 'local login' : control[provider]?.configured ? 'key available' : 'setup needed'}</span>
        </div>
        <div class="plan-form">
          <label for={`${provider}-api-key`}>{apiPlans[provider].label} API key</label>
          <input
            id={`${provider}-api-key`}
            ref={apiKeyInputs[provider]}
            type="password"
            value={apiKeys[provider]}
            autoComplete="new-password"
            placeholder={provider === 'kimi' && control?.kimi?.localAvailable ? 'Optional — local Kimi Code login detected' : control?.[provider]?.configured ? 'Key already available — leave blank to keep it' : `Paste your ${apiPlans[provider].label} API key`}
            onInput={event => setAPIKey(provider, event.currentTarget.value)}
          />
          <p>The key stays in private local files and is never returned to this page. QuotaDeck configures the official Anthropic-compatible endpoint <code>{control?.[provider]?.endpoint ?? apiPlans[provider].endpoint}</code>.</p>
          {provider === 'kimi' && <p>{control?.kimi?.localAvailable ? 'Your local Kimi Code login is available. Leave the key blank to use it; QuotaDeck renews the connection automatically.' : 'Sign in with kimi login to use your local Kimi Code connection automatically, or enter a Kimi Code API key.'}</p>}
          <div class="plan-actions">
            <button class="quiet-button" disabled={!control || busy !== '' || apiKeys[provider].trim() === ''} onClick={() => void configureAPIPlan(provider, false)}>{busy === `${provider}-save` ? 'Saving…' : 'Save key'}</button>
            <button class="refresh-button" disabled={!control || busy !== '' || (!control?.[provider]?.configured && apiKeys[provider].trim() === '') || (isPlanSelected(control, provider) && apiKeys[provider].trim() === '')} onClick={() => void configureAPIPlan(provider, true)}>
              {busy === `${provider}-activate` ? 'Configuring…' : isPlanSelected(control, provider) ? (apiKeys[provider].trim() === '' ? 'Selected' : 'Update active key') : provider === 'kimi' && control?.kimi?.source === 'kimi-cli' && apiKeys.kimi.trim() === '' ? 'Use Kimi K3' : `Save & use ${apiPlans[provider].label}${provider === 'kimi' ? ' K3' : ''}`}
            </button>
          </div>
          <a href={apiPlans[provider].keyURL} target="_blank" rel="noreferrer">Open {apiPlans[provider].label} API key management ↗</a>
        </div>
      </section>)}
    </section>
  )
}

function WindowRow({ window, mode, now, constrained }: { window: QuotaWindow; mode: 'used' | 'remaining'; now: number; constrained: boolean }) {
  const rawUsed = window.usedPercent ?? percentageFromValues(window)
  const used = rawUsed == null ? null : clamp(rawUsed)
  const percentage = used == null ? null : mode === 'used' ? used : 100 - used
  const reset = window.resetsAt
    ? new Date(window.resetsAt).getTime() <= now ? 'Reset time passed · awaiting refresh' : relativeTime(window.resetsAt, now)
    : 'No reset supplied'
  const title = window.resetsAt ? new Date(window.resetsAt).toLocaleString() : undefined
  const context = windowContext(window)
  return (
    <div class={constrained ? 'window-row constrained' : 'window-row'}>
      <div class="window-title">
        <div><strong>{window.label}</strong>{context && <span class="window-context">{context}</span>}</div>
        {constrained && <em>tightest</em>}
      </div>
      <div class="meter-line">
        <div class="meter" role="progressbar" aria-label={`${window.label} ${mode}`} aria-valuetext={percentage == null ? 'Unknown' : `${Math.round(percentage)}% ${mode}`} aria-valuenow={percentage == null ? undefined : Math.round(percentage)} aria-valuemin={0} aria-valuemax={100}>
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

function windowContext(window: QuotaWindow): string {
  const scope = window.scope?.trim()
  if (scope && scope.toLowerCase() !== 'all' && !window.label.toLowerCase().includes(scope.toLowerCase())) return scope
  if (window.kind && !['quota', 'rate-limit'].includes(window.kind)) return window.kind
  return ''
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
