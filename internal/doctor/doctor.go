package doctor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

type Report struct {
	Version    string        `json:"version"`
	Generated  time.Time     `json:"generatedAt"`
	ConfigPath string        `json:"configPath"`
	Database   string        `json:"databasePath"`
	Tools      []Tool        `json:"tools"`
	Sources    []SourceCheck `json:"sources"`
}

type Tool struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

type SourceCheck struct {
	Provider string            `json:"provider"`
	Source   string            `json:"source"`
	Accepted bool              `json:"accepted"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Collector struct {
	Config     config.Config
	ConfigPath string
	Version    string
}

func (c Collector) Collect(ctx context.Context) Report {
	report := Report{
		Version: c.Version, Generated: time.Now().UTC(), ConfigPath: c.ConfigPath,
		Database: c.Config.Storage.Database,
	}
	selfPath, _ := os.Executable()
	report.Tools = append(report.Tools, Tool{Name: "quotadeck", Present: true, Path: selfPath, Version: "quotadeck " + c.Version})
	for _, name := range []string{"cswap", "codex", "codexbar"} {
		report.Tools = append(report.Tools, inspectTool(ctx, name))
	}
	report.Sources = append(report.Sources, c.claudeSources()...)
	report.Sources = append(report.Sources, c.zaiSources()...)
	report.Sources = append(report.Sources, c.codexSources()...)
	return report
}

func (c Collector) claudeSources() []SourceCheck {
	if !c.Config.Providers.Claude.Enabled {
		return []SourceCheck{{Provider: "claude", Source: "cswap", Accepted: false, Reason: "provider disabled"}}
	}
	path, err := exec.LookPath(c.Config.Providers.Claude.Binary)
	if err != nil {
		return []SourceCheck{{Provider: "claude", Source: "cswap", Accepted: false, Reason: "cswap executable not found"}}
	}
	return []SourceCheck{{Provider: "claude", Source: "cswap", Accepted: c.Config.Providers.Claude.Enabled, Reason: "canonical multi-account source", Metadata: map[string]string{"path": path}}}
}

func (c Collector) zaiSources() []SourceCheck {
	var checks []SourceCheck
	for _, account := range c.Config.Providers.ZAI.Accounts {
		present := strings.TrimSpace(os.Getenv(account.KeyEnv)) != ""
		reason := "referenced environment variable is absent"
		if !c.Config.Providers.ZAI.Enabled {
			reason = "provider disabled"
		} else if present {
			reason = "configured environment reference is present"
		}
		checks = append(checks, SourceCheck{Provider: "zai", Source: "environment", Accepted: c.Config.Providers.ZAI.Enabled && present, Reason: reason, Metadata: map[string]string{"keyEnv": account.KeyEnv, "secretPresent": boolString(present)}})
	}
	if len(c.Config.Providers.ZAI.Accounts) == 0 {
		for _, key := range []string{"ZAI_API_KEY", "GLM_API_KEY"} {
			present := strings.TrimSpace(os.Getenv(key)) != ""
			reason := presentReason(present)
			if !c.Config.Providers.ZAI.Enabled {
				reason = "provider disabled"
			}
			checks = append(checks, SourceCheck{Provider: "zai", Source: "environment", Accepted: c.Config.Providers.ZAI.Enabled && present, Reason: reason, Metadata: map[string]string{"keyEnv": key, "secretPresent": boolString(present)}})
		}
	}
	paths := append([]string(nil), c.Config.Providers.ZAI.SettingsPaths...)
	if len(paths) == 0 {
		paths = []string{"~/.claude/settings.json"}
	}
	for _, rawPath := range paths {
		path := config.ExpandPath(rawPath)
		baseURL, hasToken, err := inspectClaudeSettings(path)
		accepted := c.Config.Providers.ZAI.Enabled && err == nil && hasToken && recognizedZAI(baseURL)
		reason := "file missing or invalid"
		if !c.Config.Providers.ZAI.Enabled {
			reason = "provider disabled"
		} else if err == nil && !hasToken {
			reason = "ANTHROPIC_AUTH_TOKEN is absent"
		} else if err == nil && !recognizedZAI(baseURL) {
			reason = "ANTHROPIC_BASE_URL is not a recognized Z.ai endpoint"
		} else if accepted {
			reason = "recognized Z.ai endpoint with a credential present"
		}
		checks = append(checks, SourceCheck{Provider: "zai", Source: "claude-settings", Accepted: accepted, Reason: reason, Metadata: map[string]string{"path": path, "baseURL": baseURL, "secretPresent": boolString(hasToken)}})
	}
	return checks
}

func (c Collector) codexSources() []SourceCheck {
	accounts := append([]config.CodexAccountConfig(nil), c.Config.Providers.Codex.Accounts...)
	if len(accounts) == 0 {
		accounts = []config.CodexAccountConfig{{Label: "Codex", Home: "~/.codex"}}
	}
	checks := make([]SourceCheck, 0, len(accounts))
	for _, account := range accounts {
		home := config.ExpandPath(account.Home)
		_, err := os.Stat(filepath.Join(home, "auth.json"))
		present := err == nil
		reason := presentReason(present)
		if !c.Config.Providers.Codex.Enabled {
			reason = "provider disabled"
		}
		checks = append(checks, SourceCheck{Provider: "codex", Source: "CODEX_HOME", Accepted: c.Config.Providers.Codex.Enabled && present, Reason: reason, Metadata: map[string]string{"home": home, "authPresent": boolString(present)}})
	}
	return checks
}

func inspectTool(ctx context.Context, name string) Tool {
	path, err := exec.LookPath(name)
	if err != nil {
		return Tool{Name: name}
	}
	tool := Tool{Name: name, Present: true, Path: path}
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, path, "--version").CombinedOutput()
	if err == nil {
		version := ""
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(strings.ToLower(line), "warning:") {
				continue
			}
			version = line
			break
		}
		if len(version) > 160 {
			version = version[:160]
		}
		tool.Version = version
	}
	return tool
}

func inspectClaudeSettings(path string) (string, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var root struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", false, err
	}
	baseURL, _ := root.Env["ANTHROPIC_BASE_URL"].(string)
	token, _ := root.Env["ANTHROPIC_AUTH_TOKEN"].(string)
	return strings.TrimSpace(baseURL), strings.TrimSpace(token) != "", nil
}

func recognizedZAI(baseURL string) bool {
	lower := strings.ToLower(baseURL)
	return strings.HasPrefix(lower, "https://") && (strings.Contains(lower, "z.ai/") || strings.Contains(lower, "bigmodel.cn/"))
}

func presentReason(present bool) string {
	if present {
		return "credential metadata detected"
	}
	return "credential metadata not detected"
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
