//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so it outlives the terminal.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
