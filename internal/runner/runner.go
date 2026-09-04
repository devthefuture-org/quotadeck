package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxOutput = 8 << 20

type Result struct {
	Stdout []byte
	Stderr string
}

type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) LookPath(name string) (string, error) { return LookPath(name) }

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	path, err := LookPath(name)
	if err != nil {
		return Result{}, fmt.Errorf("command not found: %w", err)
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Env = CommandEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &stdout, limit: maxOutput}
	command.Stderr = &limitedWriter{buffer: &stderr, limit: 64 << 10}
	err = command.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: Redact(stderr.String())}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("command timed out")
	}
	if err != nil {
		return result, fmt.Errorf("command failed: %w", err)
	}
	return result, nil
}

// CommandEnvironment preserves the process environment while making the same
// per-user binary directories available to child processes. This matters for
// script launchers such as "#!/usr/bin/env node": resolving the launcher itself
// is not enough when its interpreter lives beside it outside systemd's PATH.
func CommandEnvironment() []string {
	environment := os.Environ()
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return environment
	}
	directories := userBinaryDirectories(home)
	pathIndex := -1
	for index, item := range environment {
		if strings.HasPrefix(item, "PATH=") {
			pathIndex = index
			directories = append(directories, filepath.SplitList(strings.TrimPrefix(item, "PATH="))...)
			break
		}
	}
	pathValue := "PATH=" + strings.Join(uniquePaths(directories), string(os.PathListSeparator))
	if pathIndex >= 0 {
		environment[pathIndex] = pathValue
		return environment
	}
	return append(environment, pathValue)
}

// LookPath first follows the process PATH, then checks common per-user binary
// directories. Graphical launchers and systemd user services often start with
// a smaller PATH than an interactive shell, even though the same user-installed
// tools are available under HOME.
func LookPath(name string) (string, error) {
	path, pathErr := exec.LookPath(name)
	if pathErr == nil {
		return path, nil
	}
	if filepath.Base(name) != name {
		return "", pathErr
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", pathErr
	}
	for _, directory := range userBinaryDirectories(home) {
		candidate := filepath.Join(directory, name)
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", pathErr
}

func userBinaryDirectories(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".vite-plus", "bin"),
		filepath.Join(home, ".local", "share", "pnpm"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, "bin"),
		"/home/linuxbrew/.linuxbrew/bin",
		"/opt/homebrew/bin",
	}
}

func uniquePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result
}

func Redact(message string) string {
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "api key") {
			lines[index] = "<redacted>"
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = w.buffer.Write(data[:remaining])
		return len(data), nil
	}
	return w.buffer.Write(data)
}
