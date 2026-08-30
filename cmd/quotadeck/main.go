package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/application"
	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/service"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "quotadeck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(nil)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "doctor":
		return doctorCommand(args[1:])
	case "status":
		return apiCommand(args[1:], false)
	case "refresh":
		return apiCommand(args[1:], true)
	case "service":
		return serviceCommand(args[1:])
	case "version", "--version", "-v":
		fmt.Println("quotadeck", version)
		return nil
	case "help", "--help", "-h":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultPath(), "configuration file")
	bind := flags.String("bind", "", "loopback bind address")
	port := flags.Int("port", 0, "HTTP port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *bind != "" {
		cfg.Server.Bind = *bind
	}
	if *port != 0 {
		cfg.Server.Port = *port
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	runtime, err := application.New(cfg, *configPath, version)
	if err != nil {
		return err
	}
	defer runtime.Close()
	address := cfg.Server.Bind + ":" + strconv.Itoa(cfg.Server.Port)
	server := &http.Server{
		Addr: address, Handler: runtime.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime.Start(ctx)
	serverErrors := make(chan error, 1)
	go func() {
		fmt.Printf("QuotaDeck listening on http://%s\n", address)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func doctorCommand(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultPath(), "configuration file")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	report := (doctor.Collector{Config: cfg, ConfigPath: *configPath, Version: version}).Collect(context.Background())
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	fmt.Printf("QuotaDeck %s\nConfig: %s\nDatabase: %s\n\nTools:\n", report.Version, report.ConfigPath, report.Database)
	for _, tool := range report.Tools {
		status := "missing"
		if tool.Present {
			status = tool.Path
			if tool.Version != "" {
				status += " (" + tool.Version + ")"
			}
		}
		fmt.Printf("  %-10s %s\n", tool.Name, status)
	}
	fmt.Println("\nSources:")
	for _, source := range report.Sources {
		status := "rejected"
		if source.Accepted {
			status = "accepted"
		}
		fmt.Printf("  %-7s %-16s %-8s %s\n", source.Provider, source.Source, status, source.Reason)
	}
	return nil
}

func apiCommand(args []string, refresh bool) error {
	name := "status"
	if refresh {
		name = "refresh"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultPath(), "configuration file")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	method, path := http.MethodGet, "/api/v1/state"
	if refresh {
		method, path = http.MethodPost, "/api/v1/refresh"
	}
	request, err := http.NewRequest(method, fmt.Sprintf("http://%s:%d%s", cfg.Server.Bind, cfg.Server.Port, path), nil)
	if err != nil {
		return err
	}
	if refresh {
		request.Header.Set("X-QuotaDeck-Request", "refresh")
	}
	client := &http.Client{Timeout: 35 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("QuotaDeck server is not reachable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("server returned HTTP %d", response.StatusCode)
	}
	if *asJSON || !refresh {
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			return errors.New("server returned invalid JSON")
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	fmt.Println("refresh completed")
	return nil
}

func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("service requires install, status, or uninstall")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	switch args[0] {
	case "install":
		if len(args) < 2 || args[1] != "--user" {
			return errors.New("service install requires --user")
		}
		return service.InstallUser(ctx)
	case "status":
		return service.Status(ctx)
	case "uninstall":
		if len(args) < 2 || args[1] != "--user" {
			return errors.New("service uninstall requires --user")
		}
		return service.UninstallUser(ctx)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func printHelp() {
	fmt.Print(`QuotaDeck - local multi-provider AI quota dashboard

Usage:
  quotadeck serve [--bind 127.0.0.1] [--port 9211]
  quotadeck doctor [--json]
  quotadeck refresh
  quotadeck status [--json]
  quotadeck service install --user
  quotadeck service status
  quotadeck service uninstall --user
`)
}
