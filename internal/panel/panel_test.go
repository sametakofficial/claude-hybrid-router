package panel

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/proxy"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

func setup(t *testing.T) (*Panel, http.Handler, string) {
	t.Setenv("CLAUDE_HYBRID_HOME", t.TempDir())
	st, err := config.Open(filepath.Join(config.Home(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(config.Home(), "agents")
	st.Update(func(c *config.Config) error {
		c.Server.AgentsDir = agentsDir
		c.Providers = []config.Provider{{ID: "gpt", Type: "openai", BaseURL: "https://x/v1", APIKey: "sk-secret-123456789", Enabled: true}}
		return nil
	})
	rec := stats.New(10, nil)
	p := &Panel{Store: st, Rec: rec, Proxy: proxy.New(st.Current, rec), Started: time.Now(), Client: offlineClient(t)}
	return p, p.Handler(), agentsDir
}

// offlineClient lets requests reach httptest servers on loopback only; any
// other host fails at once, so tests never touch the network.
func offlineClient(t *testing.T) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if h := r.URL.Hostname(); h != "127.0.0.1" && h != "localhost" {
			return nil, fmt.Errorf("offline test: %s not allowed", r.URL.Host)
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func do(h http.Handler, method, path, body string, hdr map[string]string) (int, string) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:3458"
	r.Header.Set("X-CHR", "1")
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
		} else if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Body)
	return w.Code, string(b)
}

func TestGuard(t *testing.T) {
	_, h, _ := setup(t)
	if code, _ := do(h, "GET", "/api/status", "", map[string]string{"X-CHR": ""}); code != 403 {
		t.Fatalf("missing X-CHR: %d", code)
	}
	if code, _ := do(h, "GET", "/api/status", "", map[string]string{"Host": "evil.example:3458"}); code != 403 {
		t.Fatalf("non-loopback host: %d", code)
	}
	if code, _ := do(h, "GET", "/", "", nil); code != 200 {
		t.Fatalf("index: %d", code)
	}
}

func TestKeysAreMaskedAndKept(t *testing.T) {
	p, h, _ := setup(t)
	_, out := do(h, "GET", "/api/providers", "", nil)
	if strings.Contains(out, "secret") || !strings.Contains(out, `"has_api_key":true`) {
		t.Fatalf("key leaked or missing: %s", out)
	}
	code, out := do(h, "PUT", "/api/providers/gpt", `{"name":"GPT","type":"openai","base_url":"https://y/v1","api_key":"sk-…6789","enabled":true}`, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if pr, _ := p.Store.Get().Provider("gpt"); pr.APIKey != "sk-secret-123456789" || pr.BaseURL != "https://y/v1" {
		t.Fatalf("masked key must keep the stored key: %+v", pr)
	}
	_, raw := do(h, "GET", "/api/config/raw", "", nil)
	if strings.Contains(raw, "secret") {
		t.Fatal("raw config leaks key")
	}
}

func TestAgentLifecycle(t *testing.T) {
	p, h, dir := setup(t)
	code, out := do(h, "PUT", "/api/agents/coder", `{"name":"coder","description":"Writes code","prompt":"You code.","route":{"provider":"gpt","model":"gpt-5","system_mode":"keep"}}`, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "coder.md"))
	if !strings.Contains(string(data), "<!-- claude-hybrid:agent=coder -->") {
		t.Fatalf("marker missing:\n%s", data)
	}
	if rt := p.Store.Get().Agents["coder"]; rt.Model != "gpt-5" {
		t.Fatalf("route not saved: %+v", rt)
	}
	// rename + switch back to Anthropic
	code, out = do(h, "PUT", "/api/agents/coder", `{"name":"coder2","description":"Writes code","prompt":"You code.","route":{"provider":""}}`, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if _, ok := p.Store.Get().Agents["coder"]; ok || len(p.Store.Get().Agents) != 0 {
		t.Fatal("old route should be gone")
	}
	if code, out := do(h, "DELETE", "/api/providers/gpt", "", nil); code != 200 {
		t.Fatalf("unused provider should delete: %d %s", code, out)
	}
	code, out = do(h, "DELETE", "/api/agents/coder2", "", nil)
	if code != 200 || !strings.Contains(out, "trash") {
		t.Fatalf("%d %s", code, out)
	}
	if code, _ := do(h, "PUT", "/api/agents/..%2fevil", `{"name":"../evil","description":"x"}`, nil); code != 400 {
		t.Fatalf("path traversal must be rejected, got %d", code)
	}
}

func TestDiscoverAndCreate(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k-123456789012" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"data":[{"id":"m1"},{"id":"m2"},{"id":"m1"}]}`)
	}))
	defer models.Close()
	p, h, _ := setup(t)
	code, out := do(h, "POST", "/api/discover", `{"type":"openai","base_url":"`+models.URL+`/v1","api_key":"k-123456789012"}`, nil)
	if code != 200 || out != `{"models":["m1","m2"]}`+"\n" {
		t.Fatalf("%d %s", code, out)
	}
	// empty models on create -> auto-discovered
	code, out = do(h, "PUT", "/api/providers/new?create=1", `{"name":"New","type":"openai","base_url":"`+models.URL+`/v1","api_key":"k-123456789012","enabled":true}`, nil)
	if code != 200 || !strings.Contains(out, `"models":["m1","m2"]`) {
		t.Fatalf("%d %s", code, out)
	}
	if code, _ := do(h, "PUT", "/api/providers/new?create=1", `{"name":"New","type":"openai","base_url":"`+models.URL+`/v1","enabled":true}`, nil); code != 400 {
		t.Fatalf("duplicate create should fail, got %d", code)
	}
	// discover with masked key uses the stored one
	if code, out := do(h, "POST", "/api/discover", `{"id":"new","type":"openai","base_url":"`+models.URL+`/v1","api_key":"k-1…9012"}`, nil); code != 200 {
		t.Fatalf("masked key discover: %d %s", code, out)
	}
	code, out = do(h, "POST", "/api/providers/new/refresh", "", nil)
	if code != 200 || !strings.Contains(out, "m2") {
		t.Fatalf("refresh %d %s", code, out)
	}
	_ = p
	if code, out := do(h, "GET", "/api/meta", "", nil); code != 200 || !strings.Contains(out, "agent_templates") {
		t.Fatalf("meta %d", code)
	}
}

func TestPanelCannotRedirectTokenOrFiles(t *testing.T) {
	p, h, _ := setup(t)
	before := p.Store.Get().Server
	code, out := do(h, "PUT", "/api/settings", `{"proxy_listen":"127.0.0.1:3457","panel_listen":"127.0.0.1:3458","upstream":"https://evil.example","log_file":"/home/x/.bashrc","agents_dir":"/home/x/.claude","header_timeout":300}`, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	after := p.Store.Get().Server
	if after.Upstream != before.Upstream || after.LogFile != before.LogFile || after.AgentsDir != before.AgentsDir {
		t.Fatalf("file-only settings changed: %+v", after)
	}
	// stored key is not sent to a different base URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("stored key leaked to a new URL")
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	do(h, "POST", "/api/discover", `{"id":"gpt","type":"openai","base_url":"`+srv.URL+`","api_key":"sk-…6789"}`, nil)
}
