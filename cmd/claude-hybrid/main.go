// claude-hybrid runs Claude Code through a local router that sends chosen
// subagents and models to other providers. See README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/panel"
	"github.com/sametakofficial/claude-hybrid-router/internal/proxy"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/usage"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "2.0.0-dev"

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
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "start":
		err = start(true)
	case "stop":
		err = stop()
	case "restart":
		if err = stop(); err == nil {
			err = start(true)
		}
	case "status":
		err = status()
	case "panel":
		err = openPanel()
	case "env":
		err = printEnv()
	case "install-service":
		err = installService()
	case "version", "--version":
		fmt.Println("claude-hybrid", version)
	case "help", "-h", "--help":
		fmt.Print(helpText)
	case "--":
		err = launch(args[1:])
	default:
		err = launch(args)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "claude-hybrid:", err)
		os.Exit(1)
	}
}

func configPath() string { return config.Path() }
func pidPath() string    { return filepath.Join(config.Home(), "serve.pid") }

func loadConfig() (*config.Config, error) {
	st, err := config.Open(configPath())
	if err != nil {
		return nil, err
	}
	return st.Get(), nil
}

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

// ---- serve -----------------------------------------------------------------

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func serve() error {
	store, err := config.Open(configPath())
	if err != nil {
		return err
	}
	cfg := store.Get()
	lf, err := openLog(cfg.Server.LogFile)
	if err != nil {
		return err
	}
	defer lf.Close()
	log.SetOutput(io.MultiWriter(lf, os.Stderr))
	log.SetFlags(log.LstdFlags)

	us, err := usage.Open(filepath.Join(config.Home(), "usage.json"))
	if err != nil {
		return err
	}
	defer us.Close()

	rec := stats.New(1000, func(e stats.Entry) {
		if e.Source != "test" {
			cached := e.CacheReadTokens + e.CacheWriteTokens
			us.Add(usage.Record{
				Time: e.Time, Kind: e.Kind, Source: e.Source, Provider: e.Provider, Model: e.Model, ReqModel: e.ReqModel,
				Failed: e.Status >= 400 || e.Error != "",
				Tokens: usage.Tokens{Input: int64(e.InputTokens - cached), Output: int64(e.OutputTokens),
					CacheRead: int64(e.CacheReadTokens), CacheWrite: int64(e.CacheWriteTokens)},
			})
		}
		target := "anthropic"
		if e.Provider != "" {
			target = e.Provider + "/" + e.Model
		}
		// Quoted fields: model names come from requests and must not be able
		// to start new lines in the log.
		line := fmt.Sprintf("%s %q %q -> %q status=%d %dms in=%d out=%d", e.Kind, orDash(e.Source), e.ReqModel, target, e.Status, e.LatencyMS, e.InputTokens, e.OutputTokens)
		if e.Error != "" {
			line += " error=" + strconv.Quote(e.Error)
		}
		log.Print(line)
	})
	px := proxy.New(store.Current, rec)
	px.OnUpstreamHeaders = us.ObserveHeaders
	pn := &panel.Panel{Store: store, Rec: rec, Usage: us, Version: version, Started: time.Now()}

	proxyLn, err := net.Listen("tcp", cfg.Server.ProxyListen)
	if err != nil {
		if healthy(cfg.Server.ProxyListen) {
			return fmt.Errorf("already running on %s", cfg.Server.ProxyListen)
		}
		return fmt.Errorf("proxy listen %s: %w", cfg.Server.ProxyListen, err)
	}
	panelLn, err := net.Listen("tcp", cfg.Server.PanelListen)
	if err != nil {
		proxyLn.Close()
		return fmt.Errorf("panel listen %s: %w", cfg.Server.PanelListen, err)
	}
	os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(pidPath())

	// ReadHeaderTimeout only: streaming responses can run for many minutes.
	proxySrv := &http.Server{Handler: px, ReadHeaderTimeout: 30 * time.Second}
	panelSrv := &http.Server{Handler: pn.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- proxySrv.Serve(proxyLn) }()
	go func() { errc <- panelSrv.Serve(panelLn) }()
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go store.Watch(watchCtx, 2*time.Second, log.Printf)

	log.Printf("claude-hybrid %s: proxy on http://%s, panel on http://%s, config %s",
		version, cfg.Server.ProxyListen, cfg.Server.PanelListen, store.Path())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("stopping (%v)", s)
	case err := <-errc:
		log.Printf("server stopped: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	panelSrv.Shutdown(ctx)
	proxySrv.Shutdown(ctx)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---- start / stop / status -------------------------------------------------

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
	out, err := os.OpenFile(filepath.Join(config.Home(), "serve.out"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
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
	go c.Wait()
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if healthy(cfg.Server.ProxyListen) {
			if verbose {
				fmt.Printf("claude-hybrid started: panel http://%s\n", dialAddr(cfg.Server.PanelListen))
			}
			return nil
		}
	}
	b, _ := os.ReadFile(filepath.Join(config.Home(), "serve.out"))
	return fmt.Errorf("router did not start:\n%s", strings.TrimSpace(string(b)))
}

func stop() error {
	if runtime.GOOS == "linux" && exec.Command("systemctl", "--user", "is-active", "--quiet", "claude-hybrid").Run() == nil {
		return exec.Command("systemctl", "--user", "stop", "claude-hybrid").Run()
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
	if err := terminate(proc); err != nil {
		os.Remove(pidPath())
		fmt.Println("claude-hybrid was not running")
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
		state, dialAddr(cfg.Server.ProxyListen), dialAddr(cfg.Server.PanelListen), configPath(), cfg.Server.AgentsDir, cfg.Server.LogFile)
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

// ---- launch claude ---------------------------------------------------------

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
	// Ctrl+C belongs to claude; we just wait for it to exit.
	signal.Ignore(os.Interrupt)
	return c.Run()
}

// ---- systemd ---------------------------------------------------------------

const unitTemplate = `[Unit]
Description=claude-hybrid router (proxy + web panel)
After=network-online.target

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`

func installService() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("install-service is Linux only; use 'claude-hybrid start' elsewhere")
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
	unit := filepath.Join(dir, "claude-hybrid.service")
	if err := os.WriteFile(unit, []byte(fmt.Sprintf(unitTemplate, exe)), 0o644); err != nil {
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
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "claude-hybrid"}, {"restart", "claude-hybrid"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(a, " "), err, out)
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
