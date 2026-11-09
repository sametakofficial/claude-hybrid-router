//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// detach starts the child in its own session so it outlives the terminal.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// terminate asks the process to stop; graceful means it gets to clean up.
func terminate(p *os.Process) (graceful bool, err error) { return true, p.Signal(syscall.SIGTERM) }

// forwardSignals passes SIGTERM and SIGHUP on to the child (claude), so it
// can save its session when the launcher is told to stop.
func forwardSignals(child *os.Process) (stop func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range ch {
			child.Signal(s)
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}

// isOurs reports whether pid looks like a claude-hybrid process, so a stale
// pid file never makes us signal an unrelated process.
func isOurs(pid int) bool {
	if runtime.GOOS != "linux" {
		return true // no cheap check elsewhere; fall back to trusting the file
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && strings.Contains(string(b), "claude-hybrid")
}
