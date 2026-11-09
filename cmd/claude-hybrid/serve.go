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
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/panel"
	"github.com/sametakofficial/claude-hybrid-router/internal/proxy"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/usage"
)

func pidPath() string { return filepath.Join(config.Home(), "serve.pid") }

// signalContext ends on SIGINT or SIGTERM.
func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

// serve runs the proxy and the panel until ctx ends (nil) or a server
// fails (error, so the process exits non-zero and systemd restarts it).
func serve(ctx context.Context) error {
	store, err := config.Open(config.Path())
	if err != nil {
		return err
	}
	cfg := store.Current()
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
		cached := e.CacheReadTokens + e.CacheWriteTokens
		us.Add(usage.Record{
			Time: e.Time, Kind: e.Kind, Source: e.Source, Provider: e.Provider, Model: e.Model, ReqModel: e.ReqModel,
			Failed: e.Status >= 400 || e.Error != "",
			Tokens: usage.Tokens{Input: int64(e.InputTokens - cached), Output: int64(e.OutputTokens),
				CacheRead: int64(e.CacheReadTokens), CacheWrite: int64(e.CacheWriteTokens)},
		})
		log.Print(e.LogLine())
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
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go store.Watch(watchCtx, 2*time.Second, log.Printf)

	log.Printf("claude-hybrid %s: proxy on http://%s, panel on http://%s, config %s",
		version, cfg.Server.ProxyListen, cfg.Server.PanelListen, store.Path())

	var serveErr error
	select {
	case <-ctx.Done():
		log.Printf("stopping")
	case err := <-errc:
		log.Printf("server stopped: %v", err)
		serveErr = fmt.Errorf("server stopped: %w", err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	panelSrv.Shutdown(shutdown)
	proxySrv.Shutdown(shutdown)
	return serveErr
}

const maxLogSize = 10 << 20

// rotatingLog appends to path and moves it to path.1 once it passes
// maxLogSize, also while running (it used to rotate only at startup).
type rotatingLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openLog(path string) (*rotatingLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &rotatingLog{path: path}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".1")
	}
	return l, l.open()
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	f.Chmod(0o600) // older versions created it 0644; it can hold model names and errors
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, errors.New("log closed")
	}
	if l.size+int64(len(b)) > maxLogSize {
		l.f.Close()
		os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			l.f = nil
			return 0, err
		}
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
