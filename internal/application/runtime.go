package application

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/control"
	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/httpapi"
	"github.com/devthefuture-org/quotadeck/internal/poller"
	"github.com/devthefuture-org/quotadeck/internal/provider/claudecswap"
	"github.com/devthefuture-org/quotadeck/internal/provider/codex"
	"github.com/devthefuture-org/quotadeck/internal/provider/zai"
	"github.com/devthefuture-org/quotadeck/internal/runner"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

// Runtime owns the local QuotaDeck engine and its HTTP handler. Both the CLI
// server and the desktop shell use it, so discovery and persistence behave the
// same in either entrypoint.
type Runtime struct {
	database *store.Store
	engine   *poller.Engine
	logins   *codex.LoginManager
	handler  http.Handler

	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	closeErr  error
}

func New(cfg config.Config, configPath, version string) (*Runtime, error) {
	database, err := store.Open(config.ExpandPath(cfg.Storage.Database))
	if err != nil {
		return nil, err
	}
	interval, _ := cfg.PollInterval()
	timeout, _ := cfg.PollTimeout()
	providers, codexProvider := buildProviders(cfg)
	engine := poller.New(database, providers, interval, timeout, cfg.Storage.RetentionDays)
	collector := doctor.Collector{Config: cfg, ConfigPath: configPath, Version: version}
	controller := control.New(
		cfg.Providers.Claude.Binary,
		runner.ExecRunner{},
		control.DefaultPaths(cfg.Providers.ZAI.SettingsPaths),
	)
	api := httpapi.New(engine, collector, controller)
	runtime := &Runtime{database: database, engine: engine}
	if codexProvider != nil {
		// The manager takes the provider itself, so a sign-in can never run a
		// different Codex binary than the one Fetch polls with, and both share
		// the per-home exclusion that protects auth.json.
		runtime.logins = codex.NewLoginManager(codexProvider,
			func(snapshot codex.LoginSnapshot) {
				engine.Hub().Publish(map[string]any{
					"type": "codex-login", "accountId": snapshot.AccountID,
					"loginId": snapshot.ID, "state": snapshot.State, "at": time.Now().UTC(),
				})
			},
			func() { engine.RefreshProviderAfterChange("codex") },
		)
		api = api.WithCodexLogins(runtime.logins)
	}
	runtime.handler = api.Handler()
	return runtime, nil
}

func (r *Runtime) Start(parent context.Context) {
	r.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		r.cancel = cancel
		r.engine.Start(ctx)
	})
}

func (r *Runtime) Handler() http.Handler { return r.handler }

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		// Sign-in sessions outlive the request that started them and write
		// through the engine when they end. Wait for them before the store goes
		// away, or a completion lands on a closed database.
		if r.logins != nil {
			r.logins.Close()
		}
		r.closeErr = r.database.Close()
	})
	return r.closeErr
}

// buildProviders also hands back the Codex provider, which the login manager
// needs by identity rather than by configuration.
func buildProviders(cfg config.Config) ([]domain.Provider, *codex.Provider) {
	providers := make([]domain.Provider, 0, 3)
	if cfg.Providers.Claude.Enabled {
		providers = append(providers, claudecswap.New(cfg.Providers.Claude.Binary, runner.ExecRunner{}))
	}
	if cfg.Providers.ZAI.Enabled {
		providers = append(providers, zai.New(cfg.Providers.ZAI))
	}
	var codexProvider *codex.Provider
	if cfg.Providers.Codex.Enabled {
		codexProvider = codex.New(cfg.Providers.Codex.Binary, cfg.Providers.Codex.Accounts)
		providers = append(providers, codexProvider)
	}
	return providers, codexProvider
}
