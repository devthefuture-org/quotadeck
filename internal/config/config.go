package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Polling   PollingConfig   `yaml:"polling"`
	Providers ProvidersConfig `yaml:"providers"`
}

type ServerConfig struct {
	Bind string `yaml:"bind"`
	Port int    `yaml:"port"`
}

type StorageConfig struct {
	Database      string `yaml:"database"`
	RetentionDays int    `yaml:"retentionDays"`
}

type PollingConfig struct {
	Interval string `yaml:"interval"`
	Timeout  string `yaml:"timeout"`
}

type ProvidersConfig struct {
	Claude ClaudeConfig `yaml:"claude"`
	ZAI    ZAIConfig    `yaml:"zai"`
	Codex  CodexConfig  `yaml:"codex"`
}

type ClaudeConfig struct {
	Enabled bool   `yaml:"enabled"`
	Binary  string `yaml:"binary"`
}

type ZAIConfig struct {
	Enabled        bool               `yaml:"enabled"`
	Accounts       []ZAIAccountConfig `yaml:"accounts"`
	SettingsPaths  []string           `yaml:"settingsPaths"`
	QuotaURL       string             `yaml:"quotaURL"`
	RequestTimeout string             `yaml:"requestTimeout"`
	MaxRetries     int                `yaml:"maxRetries"`
}

type ZAIAccountConfig struct {
	Label             string `yaml:"label"`
	KeyEnv            string `yaml:"keyEnv"`
	Region            string `yaml:"region"`
	OrganizationIDEnv string `yaml:"organizationIdEnv"`
	WorkspaceIDEnv    string `yaml:"workspaceIdEnv"`
}

type CodexConfig struct {
	Enabled  bool                 `yaml:"enabled"`
	Binary   string               `yaml:"binary"`
	Accounts []CodexAccountConfig `yaml:"accounts"`
}

type CodexAccountConfig struct {
	Label string `yaml:"label"`
	Home  string `yaml:"home"`
}

func Default() Config {
	dataDir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dataDir = filepath.Join(home, ".local", "share")
		} else {
			dataDir = "."
		}
	}
	return Config{
		Server:  ServerConfig{Bind: "127.0.0.1", Port: 9211},
		Storage: StorageConfig{Database: filepath.Join(dataDir, "quotadeck", "quotadeck.db"), RetentionDays: 30},
		Polling: PollingConfig{Interval: "60s", Timeout: "20s"},
		Providers: ProvidersConfig{
			Claude: ClaudeConfig{Enabled: true, Binary: "cswap"},
			ZAI:    ZAIConfig{Enabled: true, RequestTimeout: "15s", MaxRetries: 3},
			Codex:  CodexConfig{Enabled: true, Binary: "codex"},
		},
	}
}

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "config.yaml"
	}
	return filepath.Join(dir, "quotadeck", "config.yaml")
}

// DefaultCodexHome follows Codex CLI's process-level profile selection before
// falling back to the conventional home directory.
func DefaultCodexHome() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	return "~/.codex"
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(contents, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Server.Bind != "127.0.0.1" && c.Server.Bind != "::1" && c.Server.Bind != "localhost" {
		return fmt.Errorf("server.bind must be a loopback address, got %q", c.Server.Bind)
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if c.Storage.RetentionDays < 1 {
		return fmt.Errorf("storage.retentionDays must be positive")
	}
	if _, err := c.PollInterval(); err != nil {
		return err
	}
	if _, err := c.PollTimeout(); err != nil {
		return err
	}
	return nil
}

func (c Config) PollInterval() (time.Duration, error) {
	return duration(c.Polling.Interval, 60*time.Second, "polling.interval")
}

func (c Config) PollTimeout() (time.Duration, error) {
	return duration(c.Polling.Timeout, 20*time.Second, "polling.timeout")
}

func (c ZAIConfig) Timeout() time.Duration {
	value, err := duration(c.RequestTimeout, 15*time.Second, "providers.zai.requestTimeout")
	if err != nil {
		return 15 * time.Second
	}
	return value
}

func ExpandPath(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func duration(raw string, fallback time.Duration, field string) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", field)
	}
	return value, nil
}
