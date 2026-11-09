package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/proxy"
)

// loadConfig reads the config without creating it (status, env, start).
func loadConfig() (*config.Config, error) { return config.Load(config.Path()) }

// dialAddr turns a listen address into one we can connect to.
func dialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func healthy(listen string) bool {
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get("http://" + dialAddr(listen) + proxy.HealthPath)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode == 200 && strings.Contains(string(b), "claude-hybrid")
}

// start runs "serve" in the background unless the router is already up.
func start(verbose bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if healthy(cfg.Server.ProxyListen) {
		if verbose {
			fmt.Printf("claude-hybrid is running: panel http://%s\n", dialAddr(cfg.Server.PanelListen))
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return err
	}
	outPath := filepath.Join(config.Home(), "serve.out")
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	c := exec.Command(exe, "serve")
	c.Stdout, c.Stderr = out, out
	detach(c)
	if err := c.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()
wait:
	for i := 0; i < 50; i++ {
		select {
		case <-exited:
			break wait // serve gave up (port taken, bad config): show why
		case <-time.After(100 * time.Millisecond):
		}
		if healthy(cfg.Server.ProxyListen) {
			if verbose {
				fmt.Printf("claude-hybrid started: panel http://%s\n", dialAddr(cfg.Server.PanelListen))
			}
			return nil
		}
	}
	b, _ := os.ReadFile(outPath)
	return fmt.Errorf("router did not start:\n%s", strings.TrimSpace(string(b)))
}

// managedBySystemd reports whether this router instance is the systemd user
// service. The unit always runs with the default home, so an instance with
// CLAUDE_HYBRID_HOME set elsewhere is never treated as the service.
func managedBySystemd() bool {
	if runtime.GOOS != "linux" || os.Getenv("CLAUDE_HYBRID_HOME") != "" {
		return false
	}
	return exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
}

func stop() error {
	if managedBySystemd() {
		if err := systemctl("stop", serviceName); err != nil {
			return err
		}
		fmt.Println("claude-hybrid stopped (systemd service)")
		return nil
	}
	b, err := os.ReadFile(pidPath())
	if err != nil {
		fmt.Println("claude-hybrid is not running")
		return nil
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	proc, err := os.FindProcess(pid)
	if err != nil || pid <= 0 || !isOurs(pid) {
		os.Remove(pidPath())
		fmt.Println("claude-hybrid is not running (removed a stale pid file)")
		return nil
	}
	graceful, err := terminate(proc)
	if err != nil {
		os.Remove(pidPath())
		fmt.Println("claude-hybrid was not running")
		return nil
	}
	if !graceful {
		// Killed outright (Windows): it cannot remove its own pid file.
		os.Remove(pidPath())
		fmt.Println("claude-hybrid stopped")
		return nil
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(pidPath()); err != nil {
			fmt.Println("claude-hybrid stopped")
			return nil
		}
	}
	return fmt.Errorf("process %d did not stop", pid)
}

// restart restarts the systemd service in place (stopping it and starting
// a detached copy would leave an unmanaged router running), else stop+start.
func restart() error {
	if managedBySystemd() {
		if err := systemctl("restart", serviceName); err != nil {
			return err
		}
		return status()
	}
	if err := stop(); err != nil {
		return err
	}
	return start(true)
}

func status() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	state := "stopped"
	if healthy(cfg.Server.ProxyListen) {
		state = "running"
	}
	fmt.Printf("state:   %s\nproxy:   http://%s\npanel:   http://%s\nconfig:  %s\nagents:  %s\nlog:     %s\n",
		state, dialAddr(cfg.Server.ProxyListen), dialAddr(cfg.Server.PanelListen), config.Path(), cfg.Server.AgentsDir, cfg.Server.LogFile)
	fmt.Printf("routes:  %d providers, %d routed agents, %d rules\n", len(cfg.Providers), len(cfg.Agents), len(cfg.Rules))
	return nil
}

func openPanel() error {
	if err := start(false); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	url := "http://" + dialAddr(cfg.Server.PanelListen) + "/"
	fmt.Println(url)
	opener := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		opener = "open"
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	_ = exec.Command(opener, url).Start()
	return nil
}

func printEnv() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	fmt.Printf("export ANTHROPIC_BASE_URL=http://%s\n", dialAddr(cfg.Server.ProxyListen))
	return nil
}
