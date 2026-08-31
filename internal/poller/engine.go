package poller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/runner"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

var ErrRefreshInProgress = errors.New("refresh already in progress")

type Engine struct {
	store      *store.Store
	providers  []domain.Provider
	interval   time.Duration
	timeout    time.Duration
	retention  time.Duration
	hub        *Hub
	refreshing atomic.Bool

	mu       sync.Mutex
	provider map[string]*sync.Mutex
}

func New(database *store.Store, providers []domain.Provider, interval, timeout time.Duration, retentionDays int) *Engine {
	locks := make(map[string]*sync.Mutex, len(providers))
	for _, provider := range providers {
		locks[provider.ID()] = &sync.Mutex{}
	}
	return &Engine{
		store: database, providers: providers, interval: interval, timeout: timeout,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		hub:       NewHub(), provider: locks,
	}
}

func (e *Engine) Hub() *Hub { return e.hub }

func (e *Engine) Providers() []domain.ProviderInfo {
	result := make([]domain.ProviderInfo, 0, len(e.providers))
	for _, provider := range e.providers {
		result = append(result, domain.ProviderInfo{ID: provider.ID(), Name: provider.Name(), Enabled: true, Source: provider.ID()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (e *Engine) Start(ctx context.Context) {
	go func() {
		_ = e.Refresh(ctx)
		_, _ = e.store.Prune(ctx, time.Now().Add(-e.retention))
	}()
	for _, provider := range e.providers {
		provider := provider
		go e.providerLoop(ctx, provider)
	}
}

func (e *Engine) providerLoop(ctx context.Context, provider domain.Provider) {
	timer := time.NewTimer(jitter(e.interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.refreshProvider(ctx, provider)
			timer.Reset(jitter(e.interval))
		}
	}
}

func (e *Engine) Refresh(ctx context.Context) error {
	if !e.refreshing.CompareAndSwap(false, true) {
		return ErrRefreshInProgress
	}
	defer e.refreshing.Store(false)
	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(e.providers))
	for _, provider := range e.providers {
		provider := provider
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := e.refreshProvider(ctx, provider); err != nil {
				errorsChannel <- err
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	var failures []error
	for err := range errorsChannel {
		failures = append(failures, err)
	}
	e.hub.Publish(map[string]any{"type": "refresh-complete", "at": time.Now().UTC(), "errors": len(failures)})
	return errors.Join(failures...)
}

// RefreshProvider refreshes one provider without coupling a focused control
// action to every other quota source. Provider-level locking keeps it safe to
// call alongside the regular polling loop.
func (e *Engine) RefreshProvider(ctx context.Context, providerID string) error {
	for _, provider := range e.providers {
		if provider.ID() == providerID {
			return e.refreshProvider(ctx, provider)
		}
	}
	return fmt.Errorf("provider %q is not enabled", providerID)
}

func (e *Engine) Current(ctx context.Context) ([]domain.AccountState, error) {
	return e.store.LatestStates(ctx)
}

func (e *Engine) History(ctx context.Context, accountID string, from, to time.Time) ([]domain.Snapshot, error) {
	return e.store.History(ctx, accountID, from, to)
}

func (e *Engine) Refreshing() bool { return e.refreshing.Load() }

func (e *Engine) refreshProvider(parent context.Context, provider domain.Provider) error {
	lock := e.providerLock(provider.ID())
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()
	candidates, err := provider.Discover(ctx)
	if err != nil {
		e.markProviderUnavailable(provider.ID(), err)
		return fmt.Errorf("%s discovery: %w", provider.ID(), err)
	}
	if len(candidates) == 0 {
		err := &domain.CodedError{Code: "source_missing", Err: errors.New("no account source discovered")}
		e.markProviderUnavailable(provider.ID(), err)
		return fmt.Errorf("%s discovery: %w", provider.ID(), err)
	}
	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := e.fetchAccount(ctx, provider, candidate); err != nil {
				errorsChannel <- fmt.Errorf("%s: %w", candidate.Label, err)
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	var failures []error
	for err := range errorsChannel {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (e *Engine) fetchAccount(ctx context.Context, provider domain.Provider, candidate domain.AccountCandidate) error {
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	account, snapshot, fetchErr := provider.Fetch(ctx, candidate)
	if fetchErr != nil {
		account = domain.Account{
			ID: candidate.ID, ProviderID: candidate.ProviderID, Label: candidate.Label,
			Source: candidate.Source, SourceMeta: cloneMeta(candidate.SourceMeta),
		}
		code := domain.ErrorCode(fetchErr)
		status := domain.StatusUnavailable
		if strings.Contains(code, "auth") || strings.Contains(code, "credential") {
			status = domain.StatusAuthError
		}
		message := safeMessage(fetchErr)
		if previous, previousErr := e.store.Latest(persistCtx, candidate.ID); previousErr == nil && len(previous.Snapshot.Windows) > 0 {
			account = previous.Account
			snapshot = domain.StaleFrom(previous.Snapshot, time.Now(), status, code, message)
		} else {
			snapshot = domain.Snapshot{
				AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: status, Stale: true,
				ErrorCode: code, ErrorMessage: message, Windows: []domain.QuotaWindow{},
			}
		}
	}
	if saveErr := e.store.Save(persistCtx, account, snapshot); saveErr != nil {
		return fmt.Errorf("persist account state: %w", saveErr)
	}
	e.hub.Publish(map[string]any{
		"type": "state", "providerId": account.ProviderID, "accountId": account.ID,
		"status": snapshot.Status, "at": snapshot.FetchedAt,
	})
	return fetchErr
}

func (e *Engine) markProviderUnavailable(providerID string, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accounts, err := e.store.AccountsByProvider(ctx, providerID)
	if err != nil {
		return
	}
	code := domain.ErrorCode(cause)
	message := safeMessage(cause)
	for _, account := range accounts {
		previous, err := e.store.Latest(ctx, account.ID)
		if err != nil {
			continue
		}
		snapshot := domain.StaleFrom(previous.Snapshot, time.Now(), domain.StatusUnavailable, code, message)
		_ = e.store.Save(ctx, account, snapshot)
	}
	e.hub.Publish(map[string]any{"type": "provider-error", "providerId": providerID, "code": code, "at": time.Now().UTC()})
}

func (e *Engine) providerLock(id string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lock, ok := e.provider[id]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	e.provider[id] = lock
	return lock
}

func safeMessage(err error) string {
	message := runner.Redact(err.Error())
	if message == "" {
		return "provider unavailable"
	}
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func jitter(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Minute
	}
	spread := interval / 10
	if spread <= 0 {
		return interval
	}
	delta := time.Duration(rand.Int64N(int64(spread)*2+1)) - spread
	return interval + delta
}

func cloneMeta(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
