//go:build linux

package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// fwmark is the packet mark used to exclude the proxy's own upstream
// connections from the iptables DNAT rule. The proxy sets SO_MARK on its
// outgoing sockets, and the iptables rule skips packets with this mark.
const fwmark = "0x1f2e3d"

// redirectedIPs are the IPs we redirect via iptables DNAT.
// Resolved once at rule-add time so we don't depend on DNS at cleanup.
var redirectedIPs []string

// addIptablesRule installs iptables OUTPUT rules that redirect connections
// to api.anthropic.com:443 → 127.0.0.1:<interceptPort>, while excluding
// packets marked with our fwmark (the proxy's own upstream connections).
//
// This is necessary because Claude Code 2.1+ is a compiled Bun binary
// whose fetch() ignores HTTPS_PROXY, undici global dispatchers, and the
// proxy option in fetchOptions. iptables DNAT is the only reliable
// interception method.
func addIptablesRule(interceptPort string, logf func(string, ...interface{})) error {
	ips, err := net.LookupHost("api.anthropic.com")
	if err != nil {
		return fmt.Errorf("resolve api.anthropic.com: %w", err)
	}

	var added []string
	for _, ip := range ips {
		// Skip IPv6 (iptables is IPv4 only; ip6tables needs separate handling)
		if strings.Contains(ip, ":") {
			continue
		}
		// Rule: redirect api.anthropic.com:443 → proxy intercept port,
		// BUT skip packets that carry our fwmark (= proxy's own upstream traffic).
		cmd := exec.Command("sudo", "-n", "iptables", "-t", "nat", "-A", "OUTPUT",
			"-p", "tcp",
			"-d", ip,
			"--dport", "443",
			"-m", "mark", "!", "--mark", fwmark,
			"-j", "DNAT",
			"--to-destination", "127.0.0.1:"+interceptPort,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			logf("iptables add failed for %s: %v (%s)", ip, err, strings.TrimSpace(string(out)))
			for _, addedIP := range added {
				removeOneRule(addedIP, interceptPort, logf)
			}
			return fmt.Errorf("iptables: %w", err)
		}
		added = append(added, ip)
		logf("iptables DNAT: %s:443 → 127.0.0.1:%s (mark !%s)", ip, interceptPort, fwmark)
	}

	redirectedIPs = added
	return nil
}

// removeIptablesRule removes all DNAT rules added by addIptablesRule.
func removeIptablesRule(interceptPort string, logf func(string, ...interface{})) {
	for _, ip := range redirectedIPs {
		removeOneRule(ip, interceptPort, logf)
	}
	redirectedIPs = nil
}

func removeOneRule(ip, interceptPort string, logf func(string, ...interface{})) {
	cmd := exec.Command("sudo", "-n", "iptables", "-t", "nat", "-D", "OUTPUT",
		"-p", "tcp",
		"-d", ip,
		"--dport", "443",
		"-m", "mark", "!", "--mark", fwmark,
		"-j", "DNAT",
		"--to-destination", "127.0.0.1:"+interceptPort,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		logf("iptables remove failed for %s: %v (%s)", ip, err, strings.TrimSpace(string(out)))
	} else {
		logf("iptables DNAT removed: %s:443", ip)
	}
}
