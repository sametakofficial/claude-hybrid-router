package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const serviceName = "claude-hybrid"

const unitTemplate = `[Unit]
Description=claude-hybrid router (proxy + web panel)
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=2
UMask=0077
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// systemdQuote quotes a path for ExecStart (spaces, quotes, %).
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "%", "%%")
	return `"` + s + `"`
}

func installService() error {
	if os.Getenv("CLAUDE_HYBRID_HOME") != "" {
		return fmt.Errorf("install-service manages the default ~/.claude-hybrid instance; unset CLAUDE_HYBRID_HOME")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unit := filepath.Join(dir, serviceName+".service")
	if err := os.WriteFile(unit, []byte(fmt.Sprintf(unitTemplate, systemdQuote(exe))), 0o644); err != nil {
		return err
	}
	// A background copy started by the launcher would hold the ports.
	if b, err := os.ReadFile(pidPath()); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 && isOurs(pid) {
			if p, err := os.FindProcess(pid); err == nil {
				terminate(p)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", serviceName}, {"restart", serviceName}} {
		if err := systemctl(a...); err != nil {
			return err
		}
	}
	fmt.Println("installed", unit)
	if cfg, err := loadConfig(); err == nil {
		for i := 0; i < 30 && !healthy(cfg.Server.ProxyListen); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return status()
}
