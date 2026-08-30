package application

import (
	"context"
	"net/http"
	"sync"

	"github.com/devthefuture-org/quotadeck/internal/config"
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
	engine := poller.New(database, buildProviders(cfg), interval, timeout, cfg.Storage.RetentionDays)
	collector := doctor.Collector{Config: cfg, ConfigPath: configPath, Version: version}
	api := httpapi.New(engine, collector)
	return &Runtime{database: database, engine: engine, handler: api.Handler()}, nil
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
		r.closeErr = r.database.Close()
	})
	return r.closeErr
}

func buildProviders(cfg config.Config) []domain.Provider {
	providers := make([]domain.Provider, 0, 3)
	if cfg.Providers.Claude.Enabled {
		providers = append(providers, claudecswap.New(cfg.Providers.Claude.Binary, runner.ExecRunner{}))
	}
	if cfg.Providers.ZAI.Enabled {
		providers = append(providers, zai.New(cfg.Providers.ZAI))
	}
	if cfg.Providers.Codex.Enabled {
		providers = append(providers, codex.New(cfg.Providers.Codex.Binary, cfg.Providers.Codex.Accounts))
	}
	return providers
}
