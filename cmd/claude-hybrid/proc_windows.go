//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

// detach starts the child without a console so it outlives the terminal.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
}

func terminate(p *os.Process) error { return p.Kill() }
