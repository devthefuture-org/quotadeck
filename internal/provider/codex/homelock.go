package codex

import "sync"

// homeLocks serializes access to one CODEX_HOME.
//
// It exists because two Codex processes pointed at the same home fight over
// auth.json: a rate-limit read renews and rewrites the tokens (truncating the
// file) while a sign-in is writing the tokens meant to replace them. Polling
// and signing in must therefore never overlap on a home.
//
// The provider owns the registry so the poller and the login manager cannot
// end up guarding two different things by accident.
type homeLocks struct {
	mu   sync.Mutex
	held map[string]struct{}
}

func newHomeLocks() *homeLocks {
	return &homeLocks{held: make(map[string]struct{})}
}

// tryAcquire takes the home without waiting, reporting whether it succeeded.
// Neither caller may block: a poll must not stall for the length of a sign-in,
// and a sign-in must not queue behind a poll that is already running.
func (h *homeLocks) tryAcquire(home string) (release func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, busy := h.held[home]; busy {
		return nil, false
	}
	h.held[home] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.held, home)
			h.mu.Unlock()
		})
	}, true
}
