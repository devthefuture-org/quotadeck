//go:build !unix

package codex

import "os/exec"

func configureProcessGroup(command *exec.Cmd) {}
