// Package marker defines the routing markers claude-hybrid puts in agent
// files and finds in system prompts, and the one rule for agent names.
// config, agents and router all use it, so the three can never disagree.
package marker

import (
	"regexp"
	"strings"
)

// namePattern is the agent name grammar: what may appear in a marker, as a
// config key and as an agent file name (NAME.md).
const namePattern = `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`

var nameRE = regexp.MustCompile(`^` + namePattern + `$`)

// AgentRE matches the routing marker <!-- claude-hybrid:agent=NAME -->.
// Group 1 is the agent name.
var AgentRE = regexp.MustCompile(`<!--\s*claude-hybrid:agent=(` + namePattern + `)\s*-->`)

// LegacyRE matches the v1 marker, which carried the target URL itself:
// <!-- @proxy-local-route:af83e9 url=http://... -->. Group 1 is the URL.
var LegacyRE = regexp.MustCompile(`(?:<!--\s*)?@proxy-local-route:af83e9\s+url=(https?://[^\s>]+?)(?:\s*-->)?(?:\s|$)`)

// Agent returns the marker line for an agent.
func Agent(name string) string { return "<!-- claude-hybrid:agent=" + name + " -->" }

// ValidName reports whether s is a valid agent name (and so a safe file name).
func ValidName(s string) bool { return nameRE.MatchString(s) && !strings.Contains(s, "..") }
