package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/chatgpt"
	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/control"
)

func claudeCommand(args []string) error {
	flags := flag.NewFlagSet("claude", flag.ContinueOnError)
	configPath := flags.String("config", config.DefaultPath(), "QuotaDeck configuration file")
	model := flags.String("model", "", "ChatGPT plan model (default: first listed model)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("Claude Code is required for quotadeck claude")
	}
	key, err := chatgpt.GatewayKey(config.ExpandPath(cfg.Storage.Database))
	if err != nil {
		return chatgpt.ErrNotConnected
	}
	endpoint := chatgpt.ClaudeEndpoint(cfg.Server.Bind, cfg.Server.Port)
	models, err := claudeModels(strings.TrimSuffix(endpoint, chatgpt.GatewayPath))
	if err != nil {
		return err
	}
	if *model == "" {
		*model = models[0].Slug
	}

	settings, err := os.CreateTemp("", "quotadeck-claude-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(settings.Name())
	paths := control.DefaultPaths(cfg.Providers.ZAI.SettingsPaths)
	selection := chatgpt.SelectClaudeModels(models, *model, control.ChatGPTClassifierPreference(paths.ClaudeSettings))
	env := selection.Environment(endpoint)
	env["ANTHROPIC_API_KEY"] = key
	modelSettings := map[string]map[string]int{}
	for slug, window := range selection.ContextWindows {
		modelSettings[slug] = map[string]int{"autoCompactWindow": window}
	}
	err = json.NewEncoder(settings).Encode(map[string]any{"env": env, "apiKeyHelper": "", "modelSettings": modelSettings})
	if closeErr := settings.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	commandArgs := append([]string{"--settings", settings.Name(), "--model", *model}, flags.Args()...)
	command := exec.Command(claude, commandArgs...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Process variables are also set for Claude Code's print/bare mode.
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Env = append(os.Environ(), command.Env...)
	return command.Run()
}

func claudeModels(base string) ([]chatgpt.Model, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/control/chatgpt/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-QuotaDeck-Request", "control")
	client := &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("start the local QuotaDeck server before launching Claude Code")
	}
	defer response.Body.Close()
	var models []chatgpt.Model
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT models are unavailable (HTTP %d). Check the connection in QuotaDeck.", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&models); err != nil || len(models) == 0 {
		return nil, errors.New("OpenAI did not list any models for this ChatGPT plan")
	}
	for _, model := range models {
		if strings.TrimSpace(model.Slug) == "" {
			return nil, errors.New("OpenAI returned an invalid model catalog")
		}
	}
	return models, nil
}
