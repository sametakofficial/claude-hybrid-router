//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// detach starts the child in its own session so it outlives the terminal.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// isOurs reports whether pid looks like a claude-hybrid process, so a stale
// pid file never makes us signal an unrelated process.
func isOurs(pid int) bool {
	if runtime.GOOS != "linux" {
		return true // no cheap check elsewhere; fall back to trusting the file
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && strings.Contains(string(b), "claude-hybrid")
}
