//go:build !windows

package mcp

import "os/exec"

func detachProcess(_ *exec.Cmd) {}
