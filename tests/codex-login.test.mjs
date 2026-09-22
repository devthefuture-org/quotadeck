import assert from 'node:assert/strict'
import test from 'node:test'

import {
  acceptSnapshot,
  formatRemaining,
  needsSignIn,
  signInView,
} from '../web/src/codexLogin.js'

const NOW = Date.parse('2026-09-22T10:00:00Z')

function pending(overrides = {}) {
  return {
    loginId: 'abc',
    state: 'pending',
    mode: 'browser',
    authUrl: 'https://auth.example.invalid/x',
    startedAt: '2026-09-22T09:59:00Z',
    expiresAt: '2026-09-22T10:09:00Z',
    ...overrides,
  }
}

test('a sign-in is offered only when the daemon says authentication is missing', () => {
  assert.equal(needsSignIn({ status: 'auth_error' }), true)
  assert.equal(needsSignIn({ status: 'unavailable', errorCode: 'codex_auth_required' }), true)
  assert.equal(needsSignIn({ status: 'unavailable', errorCode: 'codex_rpc_failed' }), false)
  assert.equal(needsSignIn({ status: 'fresh' }), false)
})

test('a late response does not put a finished sign-in back into waiting', () => {
  const finished = { loginId: 'abc', state: 'completed', startedAt: '2026-09-22T09:59:00Z' }
  assert.equal(acceptSnapshot(finished, pending()), false)
})

test('an echo of an older sign-in does not displace the current one', () => {
  const current = pending({ loginId: 'new', startedAt: '2026-09-22T09:59:30Z' })
  const stale = pending({ loginId: 'old', startedAt: '2026-09-22T09:50:00Z' })
  assert.equal(acceptSnapshot(current, stale), false)
  assert.equal(acceptSnapshot(stale, current), true)
})

test('a fresh result for the same sign-in is accepted', () => {
  const finished = { loginId: 'abc', state: 'completed', startedAt: '2026-09-22T09:59:00Z' }
  assert.equal(acceptSnapshot(pending(), finished), true)
})

test('an empty state clears only a finished sign-in', () => {
  const finished = { loginId: 'abc', state: 'completed', startedAt: '2026-09-22T09:59:00Z' }
  assert.equal(acceptSnapshot(finished, { state: 'none' }), true)
  assert.equal(acceptSnapshot(pending(), { state: 'none' }), false)
})

test('a browser sign-in exposes its link and a countdown', () => {
  const view = signInView({ status: 'auth_error' }, pending(), NOW)
  assert.equal(view.kind, 'browser')
  assert.equal(view.authUrl, 'https://auth.example.invalid/x')
  assert.equal(view.canCancel, true)
  assert.equal(formatRemaining(view.remainingMs), '9:00')
})

test('a device-code sign-in exposes the code instead of a link', () => {
  const view = signInView({ status: 'auth_error' },
    pending({ mode: 'deviceCode', authUrl: '', verificationUrl: 'https://auth.example.invalid/device', userCode: 'AAAA-BBBB' }),
    NOW)
  assert.equal(view.kind, 'deviceCode')
  assert.equal(view.userCode, 'AAAA-BBBB')
  assert.equal(view.verificationUrl, 'https://auth.example.invalid/device')
})

test('a failed sign-in surfaces the reason Codex gave', () => {
  const view = signInView({ status: 'auth_error' },
    { loginId: 'abc', state: 'failed', error: 'Login was not completed' }, NOW)
  assert.equal(view.kind, 'retry')
  assert.equal(view.message, 'Login was not completed')
})

test('an expired sign-in says so rather than echoing an empty reason', () => {
  const view = signInView({ status: 'auth_error' }, { loginId: 'abc', state: 'expired' }, NOW)
  assert.equal(view.kind, 'retry')
  assert.match(view.message, /timed out/)
})

test('a healthy account offers nothing', () => {
  assert.equal(signInView({ status: 'fresh' }, null, NOW).kind, 'none')
})

test('an account needing authentication offers the sign-in', () => {
  assert.equal(signInView({ status: 'auth_error' }, null, NOW).kind, 'offer')
})

test('an elapsed countdown never goes negative', () => {
  const view = signInView({ status: 'auth_error' }, pending(), Date.parse('2026-09-22T11:00:00Z'))
  assert.equal(view.remainingMs, 0)
  assert.equal(formatRemaining(view.remainingMs), '0:00')
})
