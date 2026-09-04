package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactDropsSecretBearingLines(t *testing.T) {
	input := "request failed\nAuthorization: Bearer should-not-leak\nretry later"
	output := Redact(input)
	if strings.Contains(output, "should-not-leak") || !strings.Contains(output, "<redacted>") {
		t.Fatalf("redaction failed: %q", output)
	}
}

func TestLookPathFindsUserToolOutsideServicePath(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "user-tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	path, err := LookPath("user-tool")
	if err != nil {
		t.Fatal(err)
	}
	if path != executable {
		t.Fatalf("expected %q, got %q", executable, path)
	}
}

func TestLookPathRejectsNonExecutableUserFile(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".vite-plus", "bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "user-tool"), []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	if _, err := LookPath("user-tool"); err == nil {
		t.Fatal("expected a non-executable user file to be rejected")
	}
}

func TestRunAddsUserDirectoriesForScriptInterpreter(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".vite-plus", "bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	interpreter := filepath.Join(directory, "test-runtime")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\nprintf 'runtime-ok'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(directory, "script-tool")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env test-runtime\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	result, err := (ExecRunner{}).Run(t.Context(), "script-tool")
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "runtime-ok" {
		t.Fatalf("unexpected output %q", result.Stdout)
	}
}
