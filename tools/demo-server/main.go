// Command demo-server serves QuotaDeck with synthetic data for documentation
// screenshots and UI development. It never discovers local provider accounts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/httpapi"
	"github.com/devthefuture-org/quotadeck/internal/poller"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

type demoProvider struct {
	id   string
	name string
}

func (p demoProvider) ID() string   { return p.id }
func (p demoProvider) Name() string { return p.name }
func (p demoProvider) Discover(context.Context) ([]domain.AccountCandidate, error) {
	return nil, nil
}
func (p demoProvider) Fetch(context.Context, domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	return domain.Account{}, domain.Snapshot{}, errors.New("demo providers do not fetch")
}

func main() {
	port := flag.Int("port", 9212, "loopback port")
	flag.Parse()

	temporaryDirectory, err := os.MkdirTemp("", "quotadeck-demo-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(temporaryDirectory)

	databasePath := filepath.Join(temporaryDirectory, "demo.db")
	database, err := store.Open(databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	if err := seed(context.Background(), database, time.Now().UTC()); err != nil {
		log.Fatal(err)
	}

	providers := []domain.Provider{
		demoProvider{id: "claude", name: "Claude"},
		demoProvider{id: "codex", name: "Codex"},
		demoProvider{id: "zai", name: "Z.ai"},
	}
	engine := poller.New(database, providers, time.Hour, 5*time.Second, 1)
	demoConfig := config.Default()
	demoConfig.Storage.Database = databasePath
	demoConfig.Providers.Claude.Enabled = false
	demoConfig.Providers.Codex.Enabled = false
	demoConfig.Providers.ZAI.Enabled = false
	collector := doctor.Collector{
		Config: demoConfig, ConfigPath: filepath.Join(temporaryDirectory, "demo-config.yaml"), Version: "demo",
	}

	address := fmt.Sprintf("127.0.0.1:%d", *port)
	server := &http.Server{Addr: address, Handler: httpapi.New(engine, collector).Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("QuotaDeck demo is available at http://%s", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func seed(ctx context.Context, database *store.Store, now time.Time) error {
	resetSoon := now.Add(2*time.Hour + 18*time.Minute)
	resetTomorrow := now.Add(19*time.Hour + 40*time.Minute)
	resetWeek := now.Add(4*24*time.Hour + 7*time.Hour)
	resetMonth := now.Add(18*24*time.Hour + 5*time.Hour)
	age := int64(7)

	type fixture struct {
		account domain.Account
		windows []domain.QuotaWindow
	}
	fixtures := []fixture{
		{
			account: domain.Account{ID: "demo-claude-personal", ProviderID: "claude", Label: "Personal", Plan: "Pro", Active: true, Source: "cswap", SourceMeta: map[string]string{"slot": "1"}},
			windows: []domain.QuotaWindow{
				window("five-hour", "5-hour window", "rolling", 38, 32, &resetSoon, true),
				window("weekly", "Weekly window", "weekly", 64, 55, &resetWeek, true),
			},
		},
		{
			account: domain.Account{ID: "demo-claude-studio", ProviderID: "claude", Label: "Studio", Plan: "Max", Source: "cswap", SourceMeta: map[string]string{"slot": "2"}},
			windows: []domain.QuotaWindow{
				window("five-hour", "5-hour window", "rolling", 17, 31, &resetSoon, true),
				window("weekly", "Weekly window", "weekly", 43, 55, &resetWeek, true),
			},
		},
		{
			account: domain.Account{ID: "demo-codex-work", ProviderID: "codex", Label: "Work", Plan: "Plus", Active: true, Source: "codex app-server"},
			windows: []domain.QuotaWindow{
				window("primary", "Primary window", "rolling", 22, 30, &resetSoon, true),
				window("secondary", "Secondary window", "weekly", 47, 56, &resetWeek, true),
			},
		},
		{
			account: domain.Account{ID: "demo-zai-team", ProviderID: "zai", Label: "GLM Team", Plan: "Coding Plan", Active: true, Source: "environment"},
			windows: []domain.QuotaWindow{
				window("tokens", "Token quota", "daily", 71, 82, &resetTomorrow, false),
				window("monthly", "Monthly pool", "monthly", 42, 40, &resetMonth, true),
			},
		},
	}

	for _, item := range fixtures {
		snapshot := domain.Snapshot{
			AccountID: item.account.ID, FetchedAt: now, SourceAgeSec: &age,
			Status: domain.StatusFresh, Windows: item.windows,
		}
		if err := database.Save(ctx, item.account, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func window(id, label, kind string, used, expected float64, resetsAt *time.Time, willLast bool) domain.QuotaWindow {
	remaining := 100 - used
	return domain.QuotaWindow{
		ID: id, Label: label, Kind: kind, UsedPercent: &used, RemainingPercent: &remaining,
		ResetsAt: resetsAt, ExpectedPercent: &expected, WillLastToReset: &willLast,
	}
}
