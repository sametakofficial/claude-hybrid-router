// claude-hybrid launches a MITM routing proxy and runs claude through it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/proxy"
)

// ownFlags maps our proxy flags to whether they consume a value.
// Anything not in this set is forwarded verbatim to claude, so users can
// type `claude-hybrid --dangerously-skip-permissions /some/path` without
// the `--` separator. `--` is still honored as an explicit end-of-own-
// flags marker in case a claude flag name ever collides (e.g. --verbose).
var ownFlagTakesValue = map[string]bool{
	"port":          true,
	"bind":          true,
	"certs-dir":     true,
	"proxy-only":    false,
	"verbose":       false,
	"require-trust": false,
	"h":             false,
	"help":          false,
}

// splitArgs separates process args into (ownArgs, claudeArgs). Known flags
// from ownFlagTakesValue land in ownArgs (with their values); everything
// else — including unknown --flags, bare positional args, and the tail
// after `--` — is passed through to claude.
func splitArgs(args []string) (own, claude []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			claude = append(claude, args[i+1:]...)
			return
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			claude = append(claude, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		value := ""
		hasInlineValue := false
		if idx := strings.Index(name, "="); idx >= 0 {
			value = name[idx+1:]
			name = name[:idx]
			hasInlineValue = true
		}
		takesValue, known := ownFlagTakesValue[name]
		if !known {
			claude = append(claude, a)
			continue
		}
		own = append(own, a)
		if takesValue && !hasInlineValue {
			if i+1 < len(args) {
				i++
				own = append(own, args[i])
			}
		}
		_ = value
	}
	return
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "trust" {
		os.Exit(runTrust(os.Args[2:]))
	}

	ownArgs, claudeArgs := splitArgs(os.Args[1:])

	fs := flag.NewFlagSet("claude-hybrid", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	port := fs.Int("port", 0, "proxy listen port (0 = random)")
	bind := fs.String("bind", "127.0.0.1", "proxy bind address")
	certsDir := fs.String("certs-dir", defaultCertsDir(), "directory for CA cert/key")
	proxyOnly := fs.Bool("proxy-only", false, "run proxy without launching claude")
	verbose := fs.Bool("verbose", false, "enable verbose logging")
	requireTrust := fs.Bool("require-trust", false, "abort if CA is not installed in the system trust store")
	help := fs.Bool("help", false, "show claude-hybrid usage")
	helpShort := fs.Bool("h", false, "show claude-hybrid usage")

	if err := fs.Parse(ownArgs); err != nil {
		fmt.Fprintln(os.Stderr, "claude-hybrid:", err)
		printUsage(fs)
		os.Exit(2)
	}
	if *help || *helpShort {
		printUsage(fs)
		os.Exit(0)
	}

	// Ensure base directory exists
	baseDir := filepath.Dir(*certsDir)
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "create base dir: %v\n", err)
		os.Exit(1)
	}

	// Open log file with daily rotation. Use an exclusive lock for
	// truncation to prevent races between concurrent instances.
	logPath := filepath.Join(baseDir, "proxy.log")
	if shouldTruncateLog(logPath) {
		tryTruncateLog(logPath)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()
	sessionID := fmt.Sprintf("s%d", os.Getpid())
	log.SetOutput(logFile)
	log.SetPrefix(fmt.Sprintf("[%s] ", sessionID))

	// Rotate any pre-existing certs dir whose schema predates the current
	// code — on-disk CAs from older versions lack the extensions modern
	// TLS clients require, so a silent reuse would reintroduce the very
	// UNABLE_TO_VERIFY_LEAF_SIGNATURE errors we're fixing.
	if err := invalidateIfStale(*certsDir, log.Printf); err != nil {
		log.Fatalf("invalidate stale certs: %v", err)
	}
	if err := os.MkdirAll(*certsDir, 0700); err != nil {
		log.Fatalf("create certs dir: %v", err)
	}

	certPath, _, certPEM, keyPEM, err := ensureCA(*certsDir, log.Printf)
	if err != nil {
		log.Fatalf("ensure CA: %v", err)
	}
	// Record the schema version after the CA is on disk so future runs
	// know whether this install is current.
	if err := writeSchemaVersion(*certsDir); err != nil {
		log.Printf("warn: write schema version: %v", err)
	}

	// Startup self-check — catches cert template regressions before the
	// child process hits them over TLS.
	if err := verifyCertChain(certPEM, keyPEM); err != nil {
		log.Fatalf("cert chain self-check failed: %v", err)
	}

	// Combined CA bundle (system roots + our CA) for tools that accept a
	// single file via SSL_CERT_FILE / REQUESTS_CA_BUNDLE / etc.
	bundlePath, err := ensureBundle(*certsDir, certPEM)
	if err != nil {
		log.Fatalf("ensure bundle: %v", err)
	}

	// Load CA into the in-memory cache.
	certCache, err := mitm.NewCertCache(certPEM, keyPEM)
	if err != nil {
		log.Fatalf("create cert cache: %v", err)
	}

	// Trust-store advisory. NODE_EXTRA_CA_CERTS covers Claude Code's
	// primary HTTPS paths; system trust is still needed for OAuth
	// redirects, pip, curl, and any spawned subprocess that can't be
	// told about our CA via env vars.
	trusted := systemTrustHas(certPEM)
	if !trusted {
		msg := fmt.Sprintf("CA is not installed in the system trust store. OAuth, pip, and curl may fail. Run 'claude-hybrid trust install' (one-time, requires sudo) or pass --require-trust to hard-fail. CA path: %s", certPath)
		if *requireTrust {
			log.Fatalf("%s", msg)
		}
		log.Printf("warn: %s", msg)
		fmt.Fprintln(os.Stderr, "claude-hybrid: "+msg)
	}

	// Load config (optional)
	opts := []proxy.Option{proxy.WithVerbose(*verbose)}
	cfgPath := filepath.Join(baseDir, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil {
		cfg, err := config.LoadConfig(cfgPath)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		resolver, err := config.NewRouteResolver(cfg)
		if err != nil {
			log.Fatalf("build route resolver: %v", err)
		}
		opts = append(opts, proxy.WithRouteResolver(resolver))
		log.Printf("Loaded config from %s", cfgPath)
	} else {
		log.Printf("No config at %s — using defaults", cfgPath)
	}

	// Start proxy
	p := proxy.New(certCache, opts...)
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *bind, *port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	proxyAddr := ln.Addr().String()
	log.Printf("Proxy listening on %s (CA=%s, bundle=%s, systemTrust=%v)", proxyAddr, certPath, bundlePath, trusted)

	srv := &http.Server{Handler: p}
	go srv.Serve(ln)

	if *proxyOnly {
		log.Println("Running in proxy-only mode (Ctrl+C to stop)")
		select {}
	}

	// Launch claude with proxy env vars. claudeArgs was already assembled
	// from the pre-flag split; append any positional args our FlagSet
	// picked up (unlikely, but harmless if someone passes e.g. a path
	// after a value-less own flag). We do NOT rewrite or strip anything
	// else — claude-hybrid is a transparent wrapper, so every arg claude
	// would accept must reach it verbatim. Working directory follows the
	// shell convention (cd /dir && claude-hybrid) exactly like claude.
	claudeArgs = append(claudeArgs, fs.Args()...)

	childEnv := buildChildEnv(proxyAddr, certPath, bundlePath)
	// Debug: print the base URL being set
	for _, e := range childEnv {
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			fmt.Fprintf(os.Stderr, "claude-hybrid: %s\n", e)
		}
	}
	fmt.Fprintf(os.Stderr, "claude-hybrid: launching claude %v\n", claudeArgs)

	cmd := exec.Command("claude", claudeArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = childEnv

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}

	if err := cmd.Run(); err != nil {
		shutdown()
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("claude: %v", err)
	}
	shutdown()
}

func printUsage(fs *flag.FlagSet) {
	fmt.Fprint(os.Stderr, `Usage: claude-hybrid [proxy-flags] [claude-args...]

Transparent wrapper around 'claude' that routes all HTTPS traffic through
a local MITM proxy. Every arg claude accepts is forwarded verbatim. To
open a project directory, use the same shell idiom as plain claude:

    cd /path/to/project && claude-hybrid

Unknown flags are forwarded to claude, so '--' is only needed when a
claude flag name collides with ours (e.g. --verbose).

Examples:
  claude-hybrid
  claude-hybrid --dangerously-skip-permissions
  claude-hybrid --model opus --resume
  cd ~/myproject && claude-hybrid
  claude-hybrid --port 18900 --proxy-only
  claude-hybrid -- --verbose          # forces --verbose onto claude

Proxy flags:
`)
	fs.SetOutput(os.Stderr)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

// shouldTruncateLog returns true if the log file was last modified before today.
func shouldTruncateLog(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	now := time.Now()
	modTime := info.ModTime()
	return modTime.Year() != now.Year() || modTime.YearDay() != now.YearDay()
}

func defaultCertsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-hybrid/certs"
	}
	return filepath.Join(home, ".claude-hybrid", "certs")
}

func ensureCA(certsDir string, logf func(string, ...interface{})) (certPath, keyPath string, certPEM, keyPEM []byte, err error) {
	certPath = filepath.Join(certsDir, "ca.crt")
	keyPath = filepath.Join(certsDir, "ca.key")

	if _, statErr := os.Stat(certPath); os.IsNotExist(statErr) {
		lockPath := filepath.Join(certsDir, "ca.lock")
		lockFile, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if lockErr != nil {
			if logf != nil {
				logf("Waiting for another instance to generate CA certificate...")
			}
			for i := 0; i < 50; i++ {
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(certPath); err == nil {
					break
				}
			}
			if _, err := os.Stat(certPath); os.IsNotExist(err) {
				return "", "", nil, nil, fmt.Errorf("timed out waiting for CA certificate generation")
			}
		} else {
			lockFile.Close()
			defer os.Remove(lockPath)
			if logf != nil {
				logf("Generating MITM CA certificate...")
			}
			certPEM, keyPEM, err = mitm.GenerateCA()
			if err != nil {
				return "", "", nil, nil, err
			}
			if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
				return "", "", nil, nil, err
			}
			if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
				return "", "", nil, nil, err
			}
			if logf != nil {
				logf("CA certificate written to %s", certPath)
			}
		}
	}

	if certPEM == nil {
		certPEM, err = os.ReadFile(certPath)
		if err != nil {
			return "", "", nil, nil, err
		}
	}
	if keyPEM == nil {
		keyPEM, err = os.ReadFile(keyPath)
		if err != nil {
			return "", "", nil, nil, err
		}
	}
	return certPath, keyPath, certPEM, keyPEM, nil
}
