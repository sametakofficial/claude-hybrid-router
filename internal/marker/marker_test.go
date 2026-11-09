package marker

import (
	"strings"
	"testing"
)

func TestOneGrammar(t *testing.T) {
	long := strings.Repeat("a", 65)
	for name, want := range map[string]bool{
		"coder": true, "code-reviewer_2.v1": true, strings.Repeat("a", 64): true,
		"": false, "-x": false, ".x": false, "a/b": false, "a..b": false, "a b": false, long: false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v", name, got)
		}
		m := AgentRE.FindStringSubmatch("x " + Agent(name) + " y")
		// Every valid name round-trips through the marker; the regex never
		// accepts a name the validator rejects (except "..", which has no
		// route and so falls back to Anthropic).
		if want && (m == nil || m[1] != name) {
			t.Errorf("marker for %q not matched: %v", name, m)
		}
		if !want && m != nil && !strings.Contains(name, "..") {
			t.Errorf("marker for invalid %q matched", name)
		}
	}
}

func TestLegacy(t *testing.T) {
	m := LegacyRE.FindStringSubmatch("<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:4568 -->\nrest")
	if m == nil || m[1] != "http://127.0.0.1:4568" {
		t.Fatalf("%v", m)
	}
}
