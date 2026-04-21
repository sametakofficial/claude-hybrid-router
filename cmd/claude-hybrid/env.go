package main

import (
	"os"
	"strings"
)

// overrideVars lists environment variables we *replace* in the child process.
// Parent values are stripped first so the child never sees duplicate keys or
// a stale CA path that would race with the one we set.
var overrideVars = map[string]struct{}{
	"HTTPS_PROXY":                 {},
	"HTTP_PROXY":                  {},
	"ALL_PROXY":                   {},
	"https_proxy":                 {},
	"http_proxy":                  {},
	"all_proxy":                   {},
	"NO_PROXY":                    {},
	"no_proxy":                    {},
	"NODE_EXTRA_CA_CERTS":         {},
	"NODE_USE_SYSTEM_CA":          {},
	"NODE_TLS_REJECT_UNAUTHORIZED": {},
	"SSL_CERT_FILE":               {},
	"SSL_CERT_DIR":                {},
	"REQUESTS_CA_BUNDLE":          {},
	"CURL_CA_BUNDLE":              {},
	"PIP_CERT":                    {},
	"GIT_SSL_CAINFO":              {},
	"AWS_CA_BUNDLE":               {},
	"DENO_CERT":                   {},
	"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH": {},
	"CLAUDE_CODE_CERT_STORE":           {},
}

// buildChildEnv constructs the environment for the Claude Code subprocess.
// It inherits the parent environment, strips every variable we manage, and
// then injects the proxy + CA configuration. We set both the Node-specific
// knobs (NODE_EXTRA_CA_CERTS, NODE_USE_SYSTEM_CA) and a bundle path for
// everything else so Python, curl, git, pip and the host OS keychain all
// find the same trust anchor.
func buildChildEnv(proxyAddr, caCertPath, bundlePath string) []string {
	env := make([]string, 0, len(os.Environ())+24)
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if _, drop := overrideVars[kv[:eq]]; drop {
			continue
		}
		env = append(env, kv)
	}
	httpProxy := "http://" + proxyAddr
	env = append(env,
		"HTTPS_PROXY="+httpProxy,
		"HTTP_PROXY="+httpProxy,
		"https_proxy="+httpProxy,
		"http_proxy="+httpProxy,
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
		"NODE_EXTRA_CA_CERTS="+caCertPath,
		"NODE_USE_SYSTEM_CA=1",
		"SSL_CERT_FILE="+bundlePath,
		"REQUESTS_CA_BUNDLE="+bundlePath,
		"CURL_CA_BUNDLE="+bundlePath,
		"PIP_CERT="+bundlePath,
		"GIT_SSL_CAINFO="+bundlePath,
		"AWS_CA_BUNDLE="+bundlePath,
		"DENO_CERT="+bundlePath,
		"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH="+bundlePath,
		"CLAUDE_CODE_CERT_STORE=system,bundled",
	)
	return env
}
