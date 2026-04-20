package proxy

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
)

// opencodeSessionCache remembers the last opencode sessionID we saw for
// each subagent name, so repeated invocations of the same agent in the
// same proxy lifetime reuse one opencode session instead of starting
// fresh every turn. This is what gives a subagent "memory" across
// successive Agent-tool calls by the parent Claude.
//
// Keyed by agent name only (not request, not TCP conn). That matches
// how users experience it: "the simplifier I called a minute ago" is
// the same actor regardless of how Claude structured the request.
//
// Entries expire after opencodeSessionTTL so stale IDs from a long-ago
// conversation don't leak into a new one. TTL resets on every use.
type opencodeSessionCache struct {
	mu      sync.Mutex
	entries map[string]opencodeSessionEntry
	ttl     time.Duration
	now     func() time.Time
}

type opencodeSessionEntry struct {
	id     string
	lastAt time.Time
}

const opencodeSessionTTL = 2 * time.Hour

func newOpencodeSessionCache() *opencodeSessionCache {
	return &opencodeSessionCache{
		entries: map[string]opencodeSessionEntry{},
		ttl:     opencodeSessionTTL,
		now:     time.Now,
	}
}

// Get returns the cached sessionID for agent, or "" if none/expired.
// A hit refreshes the entry's lastAt so continued use keeps it alive.
func (c *opencodeSessionCache) Get(agent string) string {
	if agent == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[agent]
	if !ok {
		return ""
	}
	if c.now().Sub(e.lastAt) > c.ttl {
		delete(c.entries, agent)
		return ""
	}
	e.lastAt = c.now()
	c.entries[agent] = e
	return e.id
}

// Set stores sessionID for agent (overwriting any prior entry).
func (c *opencodeSessionCache) Set(agent, sessionID string) {
	if agent == "" || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[agent] = opencodeSessionEntry{id: sessionID, lastAt: c.now()}
}

// Forget removes the cache entry for agent (used when a resume attempt
// fails so the next turn starts fresh).
func (c *opencodeSessionCache) Forget(agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, agent)
}

var defaultOpencodeSessions = newOpencodeSessionCache()

// isOpencodeRun is true when the command template launches `opencode run`.
// Used to decide whether session/format rewrites apply — generic Bash
// bridges are left untouched.
// Match `opencode run` as a complete token sequence. The leading class
// covers plain invocations (`opencode run ...`) and full-path ones
// (`/usr/bin/opencode run ...`) alike. A trailing hyphen or other
// identifier char would make it a different word (`opencode-run`) —
// deliberately not matched.
var opencodeRunRE = regexp.MustCompile(`(^|[\s/])opencode\s+run(\s|$)`)

func isOpencodeRun(cmd string) bool { return opencodeRunRE.MatchString(cmd) }

// rewriteOpencodeCommand forces --format json (so we can parse sessionID
// and assistant text out of the JSON event stream on the next turn) and,
// if a session ID is cached for this agent, inserts --session <id>. The
// rewrite is conservative: user-supplied flags and the trailing prompt
// arg are left in place. We only touch what we must.
//
// The parser tolerates all forms of the existing --format flag (space,
// =, short -f is not valid for opencode so we don't handle it). If no
// --format is present we inject one right after `opencode run`.
func rewriteOpencodeCommand(cmd, sessionID string) string {
	cmd = forceJSONFormat(cmd)
	if sessionID != "" {
		cmd = injectSessionFlag(cmd, sessionID)
	}
	return cmd
}

var formatFlagRE = regexp.MustCompile(`(--format)(\s+|=)(default|json)\b`)

func forceJSONFormat(cmd string) string {
	if formatFlagRE.MatchString(cmd) {
		return formatFlagRE.ReplaceAllString(cmd, "${1}${2}json")
	}
	return opencodeRunRE.ReplaceAllStringFunc(cmd, func(match string) string {
		return strings.Replace(match, "opencode run", "opencode run --format json", 1)
	})
}

// injectSessionFlag places --session <id> right after `opencode run`.
// Doing it at the front keeps the flag before any user-supplied options
// and avoids any collision with the trailing positional prompt arg.
func injectSessionFlag(cmd, sessionID string) string {
	return opencodeRunRE.ReplaceAllStringFunc(cmd, func(match string) string {
		return strings.Replace(match, "opencode run", "opencode run --session "+sessionID, 1)
	})
}

// parseOpencodeOutput walks newline-delimited JSON events from `opencode
// run --format json` and returns (sessionID, assistantText, errored).
// Non-JSON lines (warnings, stray stderr) are skipped silently. If the
// output contains no JSON at all we return ("", raw, false) so the
// caller can fall back to treating it as plain text — preserves
// backward compatibility with the pre-sessions behaviour of this bridge.
// errored is true when any JSON event carried a non-nil Error field,
// signalling opencode itself reported a failure.
func parseOpencodeOutput(out string) (sessionID, text string, errored bool) {
	var textParts []string
	sawJSON := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev opencodeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		sawJSON = true
		if ev.SessionID != "" && sessionID == "" {
			sessionID = ev.SessionID
		}
		if ev.Error != nil {
			errored = true
		}
		if ev.Type == "text" && ev.Part.Text != "" {
			textParts = append(textParts, ev.Part.Text)
		}
	}
	if !sawJSON {
		return "", out, false
	}
	return sessionID, strings.Join(textParts, ""), errored
}

// isOpencodeFailure decides whether a Turn 2 tool_result indicates the
// opencode invocation failed such that any cached session for this
// agent should be forgotten (avoid resuming into a broken session on
// the next turn). Signals:
//   - Claude Code's Bash tool timeout prefix in the raw output
//   - no session captured AND no assistant text (opencode never ran)
//   - opencode itself emitted an error event
func isOpencodeFailure(sessionID, text, rawOut string, errored bool) bool {
	if errored {
		return true
	}
	if strings.Contains(rawOut, "Command timed out") {
		return true
	}
	if sessionID == "" && strings.TrimSpace(text) == "" {
		return true
	}
	return false
}

type opencodeEvent struct {
	Type      string             `json:"type"`
	SessionID string             `json:"sessionID"`
	Part      opencodeEventPart  `json:"part"`
	Error     *opencodeEventError `json:"error,omitempty"`
}

type opencodeEventPart struct {
	Text string `json:"text"`
}

type opencodeEventError struct {
	Name string `json:"name"`
}
