package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

// launch runs claude through the router, starting the router if needed.
func launch(args []string) error {
	if err := start(false); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH")
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			env = append(env, kv)
		}
	}
	env = append(env, "ANTHROPIC_BASE_URL=http://"+dialAddr(cfg.Server.ProxyListen))

	c := exec.Command(claude, args...)
	c.Stdin, c.Stdout, c.Stderr, c.Env = os.Stdin, os.Stdout, os.Stderr, env
	// Ctrl+C belongs to claude (it shares the terminal); we just wait.
	signal.Ignore(os.Interrupt)
	if err := c.Start(); err != nil {
		return err
	}
	stopForwarding := forwardSignals(c.Process)
	defer stopForwarding()
	return c.Wait()
}
