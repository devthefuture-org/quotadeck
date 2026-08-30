package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &stdout, limit: maxOutput}
	command.Stderr = &limitedWriter{buffer: &stderr, limit: 64 << 10}
	err := command.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: Redact(stderr.String())}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("command timed out")
	}
	if err != nil {
		return result, fmt.Errorf("command failed: %w", err)
	}
	return result, nil
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
