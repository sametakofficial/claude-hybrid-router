package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func writeConfig(t *testing.T, proxyAddr, panelAddr string) string {
	home := t.TempDir()
	t.Setenv("CLAUDE_HYBRID_HOME", home)
	cfg := fmt.Sprintf("version: 2\nserver:\n  proxy_listen: %s\n  panel_listen: %s\n  agents_dir: %s\n", proxyAddr, panelAddr, filepath.Join(home, "agents"))
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestServeStartsAndStopsCleanly(t *testing.T) {
	proxyAddr := freeAddr(t)
	home := writeConfig(t, proxyAddr, freeAddr(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx) }()
	for i := 0; i < 100 && !healthy(proxyAddr); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !healthy(proxyAddr) {
		t.Fatal("router did not come up")
	}
	if _, err := os.Stat(filepath.Join(home, "serve.pid")); err != nil {
		t.Fatal("pid file missing")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean stop must return nil: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	if fi, err := os.Stat(filepath.Join(home, "proxy.log")); err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("proxy.log must be 0600: %v %v", err, fi)
	}
}

func TestServeFailsWhenPortIsTaken(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	writeConfig(t, freeAddr(t), ln.Addr().String())
	if err := serve(context.Background()); err == nil || !strings.Contains(err.Error(), "panel listen") {
		t.Fatalf("want a listen error (non-zero exit), got %v", err)
	}
}

func TestStatusDoesNotCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_HYBRID_HOME", home)
	if err := run([]string{"env"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("env must not create config.yaml")
	}
	if managedBySystemd() {
		t.Fatal("an instance with CLAUDE_HYBRID_HOME is never the systemd service")
	}
}

func TestLogRotatesWhileRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	l, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	chunk := []byte(strings.Repeat("x", 1<<20) + "\n")
	for i := 0; i < 11; i++ {
		l.Write(chunk)
	}
	old, err1 := os.Stat(path + ".1")
	cur, err2 := os.Stat(path)
	if err1 != nil || err2 != nil || old.Size() > maxLogSize || cur.Size() >= maxLogSize {
		t.Fatalf("rotation: %v %v %v %v", err1, err2, old, cur)
	}
}
