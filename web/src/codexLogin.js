// Client-side rules for a Codex sign-in. Kept free of DOM and network so the
// decisions below can be tested directly with `node --test`.

/** @typedef {'starting'|'pending'|'completed'|'failed'|'canceled'|'expired'|'none'} LoginState */

const TERMINAL = new Set(['completed', 'failed', 'canceled', 'expired'])

/** @param {{state?: string}|null|undefined} snapshot */
export function isTerminal(snapshot) {
  return !!snapshot && TERMINAL.has(snapshot.state)
}

/** @param {{state?: string}|null|undefined} snapshot */
export function isActive(snapshot) {
  return !!snapshot && (snapshot.state === 'starting' || snapshot.state === 'pending')
}

/**
 * Decide whether an arriving snapshot should replace the one on screen.
 *
 * Responses are not ordered: the POST that starts a sign-in can land after the
 * server-sent event announcing its result, and a periodic reconciliation can
 * answer with state older than what we already have. Accepting blindly would
 * put a finished sign-in back into "waiting".
 *
 * @param {object|null} current
 * @param {object|null} incoming
 */
export function acceptSnapshot(current, incoming) {
  if (!incoming || incoming.state === 'none') return !current || isTerminal(current) ? !!incoming : false
  if (!current) return true
  if (current.loginId !== incoming.loginId) {
    // A different sign-in wins only if it started later; an echo of the
    // previous one must not displace the current attempt.
    return startedAt(incoming) >= startedAt(current)
  }
  if (isTerminal(current) && !isTerminal(incoming)) return false
  return true
}

function startedAt(snapshot) {
  const parsed = Date.parse(snapshot?.startedAt ?? '')
  return Number.isNaN(parsed) ? 0 : parsed
}

/**
 * Describe what the account card should offer.
 *
 * @param {{status?: string, errorCode?: string}} accountSnapshot
 * @param {object|null} login
 * @param {number} now epoch milliseconds
 */
export function signInView(accountSnapshot, login, now) {
  if (isActive(login)) {
    const remaining = Math.max(0, Date.parse(login.expiresAt ?? '') - now)
    return {
      kind: login.mode === 'deviceCode' ? 'deviceCode' : 'browser',
      authUrl: login.authUrl || '',
      verificationUrl: login.verificationUrl || '',
      userCode: login.userCode || '',
      loginId: login.loginId,
      remainingMs: Number.isNaN(remaining) ? 0 : remaining,
      canCancel: true,
      message: '',
    }
  }
  if (login && login.state === 'completed') {
    return { ...idleView(), kind: 'completed' }
  }
  if (isTerminal(login)) {
    return { ...idleView(), kind: 'retry', message: failureMessage(login) }
  }
  if (needsSignIn(accountSnapshot)) {
    return { ...idleView(), kind: 'offer' }
  }
  return { ...idleView(), kind: 'none' }
}

/**
 * The dashboard offers a sign-in exactly when the daemon says authentication
 * is what is missing. Never infer it from the presence of an auth file: the
 * keyring backend deletes that file on purpose while staying signed in.
 *
 * @param {{status?: string, errorCode?: string}} accountSnapshot
 */
export function needsSignIn(accountSnapshot) {
  if (!accountSnapshot) return false
  return accountSnapshot.status === 'auth_error' || accountSnapshot.errorCode === 'codex_auth_required'
}

// Every branch answers with the same shape, so a caller never has to guess
// which fields exist for which kind.
function idleView() {
  return {
    kind: 'none', authUrl: '', verificationUrl: '', userCode: '',
    loginId: '', remainingMs: 0, canCancel: false, message: '',
  }
}

function failureMessage(login) {
  if (login.state === 'expired') return 'The sign-in timed out. Try again.'
  if (login.state === 'canceled') return 'Sign-in canceled.'
  return login.error || 'The sign-in did not complete.'
}

/** @param {number} milliseconds */
export function formatRemaining(milliseconds) {
  const total = Math.max(0, Math.round(milliseconds / 1000))
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}
