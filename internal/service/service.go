package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "quotadeck.service"

func InstallUser(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve config directory: %w", err)
	}
	unitDir := filepath.Join(configDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	contents := fmt.Sprintf(`[Unit]
Description=QuotaDeck local AI quota dashboard
After=network-online.target

[Service]
Type=simple
EnvironmentFile=-%%h/.config/quotadeck/environment
ExecStart=%s serve
Restart=on-failure
RestartSec=5
NoNewPrivileges=true

[Install]
WantedBy=default.target
`, executable)
	path := filepath.Join(unitDir, unitName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return systemctl(ctx, "enable", "--now", unitName)
}

func Status(ctx context.Context) error {
	command := exec.CommandContext(ctx, "systemctl", "--user", "status", unitName, "--no-pager")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func UninstallUser(ctx context.Context) error {
	_ = systemctl(ctx, "disable", "--now", unitName)
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(configDir, "systemd", "user", unitName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	return systemctl(ctx, "daemon-reload")
}

func systemctl(ctx context.Context, args ...string) error {
	commandArgs := append([]string{"--user"}, args...)
	output, err := exec.CommandContext(ctx, "systemctl", commandArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user failed: %s", string(output))
	}
	return nil
}
