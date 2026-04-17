//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// runElevated runs argv as root. If already root, runs directly; otherwise
// prefixes with sudo. stdout/stderr/stdin are connected to the current process.
func runElevated(argv ...string) error {
	if os.Geteuid() != 0 {
		argv = append([]string{"sudo"}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
