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

// terminate kills the process: Windows has no SIGTERM, so it cannot clean up.
func terminate(p *os.Process) (graceful bool, err error) { return false, p.Kill() }

// forwardSignals: claude gets console signals itself on Windows.
func forwardSignals(*os.Process) (stop func()) { return func() {} }

func isOurs(pid int) bool { return true }
