// claude-hybrid runs Claude Code through a local router that sends chosen
// subagents and models to other providers. See README.md.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "2.3.0-dev"

const helpText = `claude-hybrid — route Claude Code subagents to any model provider

Usage:
  claude-hybrid [claude args...]   start the router if needed, then run claude through it
  claude-hybrid serve              run the router in the foreground (proxy + web panel)
  claude-hybrid start | stop | restart | status
  claude-hybrid panel              print the web panel URL and open it
  claude-hybrid env                print the export line to use the router from any shell
  claude-hybrid install-service    install and start a systemd user service (Linux)
  claude-hybrid version

Every argument that is not a claude-hybrid command goes to claude unchanged,
e.g. "claude-hybrid --resume" or "claude-hybrid -p 'hi'". Use
"claude-hybrid -- serve" to pass the word "serve" to claude.

Config: ~/.claude-hybrid/config.yaml (edit in the panel). Override the folder
with CLAUDE_HYBRID_HOME.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "claude-hybrid:", err)
		os.Exit(1)
	}
}

// run dispatches one command line.
func run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(signalContext())
	case "start":
		return start(true)
	case "stop":
		return stop()
	case "restart":
		return restart()
	case "status":
		return status()
	case "panel":
		return openPanel()
	case "env":
		return printEnv()
	case "install-service":
		return installService()
	case "version", "--version":
		fmt.Println("claude-hybrid", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(helpText)
		return nil
	case "--":
		return launch(args[1:])
	}
	return launch(args)
}
