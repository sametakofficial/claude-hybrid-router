// Package proxy implements the MITM CONNECT proxy with local model routing.
package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
)

// Proxy is an HTTP handler that handles CONNECT requests with MITM TLS.
type Proxy struct {
	certCache     *mitm.CertCache
	httpClient    *http.Client
	localClient   *http.Client
	modelResolver *config.ModelResolver
	sem           chan struct{}
	verbose       bool
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithVerbose enables verbose logging.
func WithVerbose(v bool) Option {
	return func(p *Proxy) { p.verbose = v }
}

// WithHTTPClient sets a custom HTTP client for upstream requests.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Proxy) { p.httpClient = c }
}

// WithModelResolver sets the model resolver for local routing.
func WithModelResolver(r *config.ModelResolver) Option {
	return func(p *Proxy) { p.modelResolver = r }
}

// New creates a new Proxy.
func New(cache *mitm.CertCache, opts ...Option) *Proxy {
	p := &Proxy{
		certCache: cache,
		sem:       make(chan struct{}, config.MaxProxyGoroutines),
	}
	for _, o := range opts {
		o(p)
	}
	if p.httpClient == nil {
		p.httpClient = &http.Client{
			Transport: &http.Transport{
				ForceAttemptHTTP2:     true,
				TLSClientConfig:       &tls.Config{},
				ResponseHeaderTimeout: config.UpstreamTimeout,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			// No http.Client.Timeout — it covers the entire response including
			// body reads, which kills long-running streaming responses from
			// Anthropic's API. ResponseHeaderTimeout on the Transport handles
			// the "server not responding" case without cutting off streams.
		}
	}
	if p.localClient == nil {
		egressTimeout := config.UpstreamTimeout
		if p.modelResolver != nil {
			egressTimeout = p.modelResolver.Timeout()
		}
		p.localClient = &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: egressTimeout,
			},
		}
	}
	return p
}

// ServeHTTP handles CONNECT requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}

	// Acquire semaphore (non-blocking)
	select {
	case p.sem <- struct{}{}:
	default:
		http.Error(w, "proxy overloaded", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-p.sem }()

	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		p.logVerbose("hijack error: %v", err)
		return
	}
	defer conn.Close()

	if shouldBypassMITM(host) {
		if err := p.tunnelDirect(conn, net.JoinHostPort(host, port)); err != nil {
			p.logVerbose("direct tunnel failed for %s: %v", host, err)
		}
		return
	}

	// Send 200 Connection Established
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// MITM TLS handshake
	tlsCfg, err := p.certCache.GetTLSConfig(host)
	if err != nil {
		p.logVerbose("cert generation failed for %s: %v", host, err)
		return
	}
	tlsConn := tls.Server(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		p.logVerbose("MITM TLS handshake failed for %s: %v", host, err)
		return
	}
	defer tlsConn.Close()

	p.handleTunnel(tlsConn, host, port)
}

func shouldBypassMITM(host string) bool {
	host = strings.ToLower(host)
	return host == "pypi.org" || host == "files.pythonhosted.org" || strings.HasSuffix(host, ".pythonhosted.org")
}

func (p *Proxy) tunnelDirect(client net.Conn, target string) error {
	upstream, err := net.DialTimeout("tcp", target, config.UpstreamTimeout)
	if err != nil {
		sendError(client, 502, "Bad Gateway")
		return err
	}
	defer upstream.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return err
	}

	var wg sync.WaitGroup
	copyConn := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}

	wg.Add(2)
	go copyConn(upstream, client)
	go copyConn(client, upstream)
	wg.Wait()
	return nil
}

func (p *Proxy) handleTunnel(tlsConn net.Conn, host, port string) {
	tlsConn.SetDeadline(deadlineFromNow(config.ClientRecvTimeout))
	br := bufio.NewReader(tlsConn)

	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return // Connection closed or read error
		}

		body, err := io.ReadAll(io.LimitReader(req.Body, config.MaxBodyBytes+1))
		req.Body.Close()
		if err != nil {
			sendError(tlsConn, 400, "Bad Request")
			return
		}
		if int64(len(body)) > config.MaxBodyBytes {
			sendError(tlsConn, 413, "Content Too Large")
			return
		}

		// Reset deadline for each request
		tlsConn.SetDeadline(deadlineFromNow(config.ClientRecvTimeout))

		routeModel, strippedBody := detectLocalRoute(body)

		// route_all: if no marker found but route_all is configured and this is
		// an Anthropic /v1/messages request, force-route to the configured label.
		// Non-messages endpoints (telemetry, MCP registry, etc.) pass through upstream.
		if routeModel == "" && p.modelResolver != nil && p.modelResolver.RouteAll() != "" &&
			isAPIHost(host) && req.URL.Path == "/v1/messages" {
			routeModel = p.modelResolver.RouteAll()
			strippedBody = body // no marker to strip
		}

		// System prompt override: replace the system field before forwarding.
		if routeModel != "" && p.modelResolver != nil && p.modelResolver.SystemPrompt() != "" &&
			req.Method == "POST" {
			strippedBody = rewriteSystemPrompt(strippedBody, p.modelResolver.SystemPrompt())
		}

		// Reminder injection: append <system-reminder> to messages containing tool_result.
		if routeModel != "" && p.modelResolver != nil && p.modelResolver.Reminder() != "" &&
			req.Method == "POST" {
			strippedBody = injectReminder(strippedBody, p.modelResolver.Reminder())
		}

		if routeModel != "" {
			streamMode := "non-streaming"
			var reqMeta struct {
				Stream bool `json:"stream"`
			}
			if json.Unmarshal(body, &reqMeta) == nil && reqMeta.Stream {
				streamMode = "streaming"
			}
			log.Printf("LOCAL_ROUTE %s https://%s:%s%s → model=%s (%s)",
				req.Method, host, port, req.URL.RequestURI(), routeModel, streamMode)

			var reqModel struct {
				Model string `json:"model"`
			}
			json.Unmarshal(strippedBody, &reqModel)
			p.forwardLocal(tlsConn, routeModel, strippedBody, reqModel.Model)
		} else {
			if !p.forwardUpstream(tlsConn, host, port, req, body) {
				return
			}
		}

		if req.Close {
			return
		}
	}
}

var hopByHop = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"transfer-encoding": true,
	"te":                true,
	"trailers":          true,
	"upgrade":           true,
}

func (p *Proxy) forwardUpstream(tlsConn net.Conn, host, port string, req *http.Request, body []byte) bool {
	var url string
	if port == "443" {
		url = "https://" + host + req.URL.RequestURI()
	} else {
		url = "https://" + net.JoinHostPort(host, port) + req.URL.RequestURI()
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = strings.NewReader(string(body))
	}

	upReq, err := http.NewRequest(req.Method, url, bodyReader)
	if err != nil {
		sendError(tlsConn, 502, "Bad Gateway")
		return false
	}

	// Copy headers, skip hop-by-hop
	for k, vals := range req.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			upReq.Header.Add(k, v)
		}
	}
	if len(body) > 0 {
		upReq.ContentLength = int64(len(body))
	}

	resp, err := p.httpClient.Do(upReq)
	if err != nil {
		if p.verbose || isAPIHost(host) {
			log.Printf("upstream error for %s: %v", host, err)
		}
		sendError(tlsConn, 502, "Bad Gateway")
		return false
	}
	defer resp.Body.Close()

	// Three cases for forwarding the upstream response over the HTTP/1.1
	// tunnel to the client:
	//
	// 1. Content-Length present → stream directly, client knows body size.
	// 2. SSE (text/event-stream) → stream directly, close connection after
	//    (SSE has no Content-Length and may run for minutes; buffering would
	//    block or OOM).
	// 3. Chunked / unknown length (non-SSE) → buffer, inject Content-Length
	//    so the keep-alive connection can be reused.
	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	hasCL := resp.ContentLength >= 0

	if hasCL || isSSE {
		// Stream directly
		writeResponseHeaders(tlsConn, resp)
		if _, err := io.Copy(tlsConn, resp.Body); err != nil {
			p.logVerbose("response streaming error for %s: %v", host, err)
			return false
		}
		// SSE streams have no defined end marker in HTTP/1.1 without
		// Content-Length or Transfer-Encoding, so close the connection.
		if isSSE && !hasCL {
			return false
		}
	} else {
		// Buffer body and add Content-Length for connection reuse
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxBodyBytes+1))
		if err != nil {
			p.logVerbose("response read error for %s: %v", host, err)
			return false
		}
		if int64(len(respBody)) > config.MaxBodyBytes {
			p.logVerbose("response from %s exceeded size limit", host)
			sendError(tlsConn, 502, "Bad Gateway")
			return false
		}
		writeResponseHeadersWithCL(tlsConn, resp, len(respBody))
		tlsConn.Write(respBody)
	}

	return true
}

func writeResponseHeaders(w io.Writer, resp *http.Response) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n", resp.Status) // "200 OK"
	for k, vals := range resp.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprint(w, "\r\n")
}

func writeResponseHeadersWithCL(w io.Writer, resp *http.Response, bodyLen int) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n", resp.Status)
	for k, vals := range resp.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n", bodyLen)
	fmt.Fprint(w, "\r\n")
}

// forwardLocal handles a request whose system field carried a routing marker.
// After the musistudio egress migration, the Anthropic request body is sent
// verbatim to the configured musistudio /v1/messages endpoint — only the
// `model` field is rewritten to musistudio's "<provider>,<model>" selector.
// musistudio handles Anthropic↔OpenAI translation, provider quirks, SSE rewrites,
// tool-use normalization, fallback, etc.
func (p *Proxy) forwardLocal(w io.Writer, modelLabel string, body []byte, originalModel string) {
	if p.modelResolver == nil {
		// No config — fall back to stub response
		isStreaming := false
		var data map[string]interface{}
		if json.Unmarshal(body, &data) == nil {
			if s, ok := data["stream"].(bool); ok {
				isStreaming = s
			}
		}
		sendLocalStub(w, modelLabel, isStreaming)
		return
	}

	start := time.Now()

	resolved, err := p.modelResolver.Resolve(modelLabel)
	if err != nil {
		log.Printf("model resolution failed: %v", err)
		errBody := formatError("invalid_request_error",
			fmt.Sprintf("Unknown model label %q — check ~/.claude-hybrid/config.yaml", modelLabel))
		sendAnthropicError(w, 400, errBody)
		return
	}

	// Command bridge short-circuit: return tool_use for Bash, bypass egress.
	if resolved.Command != "" {
		isStreaming := false
		var data map[string]interface{}
		if json.Unmarshal(body, &data) == nil {
			if s, ok := data["stream"].(bool); ok {
				isStreaming = s
			}
		}
		// Command providers historically stored the agent name under Model;
		// keep that contract untouched.
		p.forwardCommand(w, resolved.Command, resolved.Model, modelLabel, body, isStreaming)
		return
	}

	// Rewrite the Anthropic body for musistudio: swap `model` → "<prov>,<model>",
	// optionally cap max_tokens. Everything else passes through untouched.
	egressBody, isStreaming, err := rewriteBodyForEgress(body, resolved.Model, resolved.MaxTokens)
	if err != nil {
		log.Printf("[LOCAL_ERR:PARSE] body rewrite failed for %s: %v", modelLabel, err)
		errBody := formatError("invalid_request_error",
			fmt.Sprintf("Failed to parse request body: %v", err))
		sendAnthropicError(w, 400, errBody)
		return
	}

	endpoint := resolved.Endpoint + "/v1/messages"
	maxAttempts := 1 + config.EgressMaxRetries

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		localReq, err := http.NewRequest("POST", endpoint, strings.NewReader(string(egressBody)))
		if err != nil {
			log.Printf("failed to create egress request: %v", err)
			errBody := formatError("api_error", fmt.Sprintf("Failed to create request: %v", err))
			sendAnthropicError(w, 500, errBody)
			return
		}
		localReq.Header.Set("Content-Type", "application/json")
		localReq.Header.Set("anthropic-version", "2023-06-01")
		if resolved.APIKey != "" {
			localReq.Header.Set("x-api-key", resolved.APIKey)
		}

		resp, err := p.localClient.Do(localReq)
		if err != nil {
			cat := classifyError(err)
			// Never retry on transport-level errors (timeouts, connection refused).
			// These already waited 120s — retrying would multiply the wait.
			log.Printf("[LOCAL_ERR:%s] egress unreachable for %s: %v (%s)", cat, modelLabel, err, endpoint)
			errBody := formatError("api_error",
				fmt.Sprintf("[%s] Egress '%s' unreachable: %v (%s)", cat, modelLabel, err, endpoint))
			sendAnthropicError(w, 502, errBody)
			return
		}

		if isRetryableStatus(resp.StatusCode) && attempt < maxAttempts {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			delay := retryDelay(attempt)
			log.Printf("[LOCAL_RETRY:%d/%d] egress %s returned %d — retrying in %v (body: %s)",
				attempt, maxAttempts, modelLabel, resp.StatusCode, delay, sanitizeForLog(string(respBody)))
			time.Sleep(delay)
			continue
		}

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			sanitized := sanitizeForLog(string(respBody))
			log.Printf("[LOCAL_ERR:HTTP_%d] egress %s returned %d: %s", resp.StatusCode, modelLabel, resp.StatusCode, sanitized)
			forwarded := respBody
			if !isAnthropicError(respBody) {
				forwarded = formatError("api_error",
					fmt.Sprintf("[HTTP_%d] Egress '%s' returned %d: %s", resp.StatusCode, modelLabel, resp.StatusCode, sanitized))
			}
			code := 502
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				code = 400
			}
			sendAnthropicError(w, code, forwarded)
			return
		}

		if err := resp.Write(w); err != nil {
			resp.Body.Close()
			cat := classifyError(err)
			log.Printf("[LOCAL_ERR:%s] response write for %s: %v", cat, modelLabel, err)
			return
		}
		resp.Body.Close()
		streamTag := ""
		if isStreaming {
			streamTag = "streaming, "
		}
		retryTag := ""
		if attempt > 1 {
			retryTag = fmt.Sprintf("retry %d, ", attempt-1)
		}
		log.Printf("LOCAL_OK %s → %s/%s (%s%s%dms)",
			modelLabel, resolved.Provider, resolved.Model, retryTag, streamTag, time.Since(start).Milliseconds())
		return
	}
}

// rewriteResponseModel replaces the "model" field in an Anthropic JSON response
// with the original Claude model name so Claude Code accepts it.
func rewriteResponseModel(body []byte, model string) []byte {
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil {
		return body
	}
	if _, ok := data["model"]; ok {
		data["model"] = model
		if out, err := json.Marshal(data); err == nil {
			return out
		}
	}
	return body
}

// injectReminder appends a <system-reminder> text block to every user message
// that contains a tool_result content block. This ensures the reminder is
// present on every tool call round-trip back to the API.
func injectReminder(body []byte, reminder string) []byte {
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil {
		return body
	}
	messages, ok := data["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return body
	}
	reminderBlock := map[string]interface{}{
		"type": "text",
		"text": "<system-reminder>\n" + reminder + "\n</system-reminder>",
	}
	modified := false
	for i, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok || m["role"] != "user" {
			continue
		}
		content, ok := m["content"].([]interface{})
		if !ok {
			continue
		}
		hasToolResult := false
		for _, block := range content {
			if bm, ok := block.(map[string]interface{}); ok {
				if bm["type"] == "tool_result" {
					hasToolResult = true
					break
				}
			}
		}
		if hasToolResult {
			m["content"] = append(content, reminderBlock)
			messages[i] = m
			modified = true
		}
	}
	if !modified {
		return body
	}
	data["messages"] = messages
	out, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return out
}

// rewriteSystemPrompt replaces the system field in the request body.
func rewriteSystemPrompt(body []byte, newSystem string) []byte {
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil {
		return body
	}
	data["system"] = newSystem
	out, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return out
}

var modelFieldRe = regexp.MustCompile(`"model"\s*:\s*"[^"]*"`)

// streamWithModelRewrite reads the first chunk from src, replaces the model
// field, writes it to dst, then copies the rest with io.Copy. No buffering.
func streamWithModelRewrite(dst io.Writer, src io.Reader, targetModel string) error {
	buf := make([]byte, 16*1024)
	n, err := src.Read(buf)
	if n > 0 {
		chunk := buf[:n]
		chunk = modelFieldRe.ReplaceAll(chunk, []byte(`"model":"`+targetModel+`"`))
		if _, werr := dst.Write(chunk); werr != nil {
			return werr
		}
	}
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

// rewriteBodyForEgress swaps the model field for musistudio's "<provider>,<model>"
// format and optionally caps max_tokens. Returns the new body, the stream flag,
// and any parse error. Unknown fields are preserved verbatim.
func rewriteBodyForEgress(body []byte, egressModel string, maxTokensCap int) ([]byte, bool, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, false, err
	}
	data["model"] = egressModel
	if maxTokensCap > 0 {
		// Only cap if the client asked for more — never raise.
		if current, ok := data["max_tokens"].(float64); ok {
			if int(current) > maxTokensCap {
				data["max_tokens"] = maxTokensCap
			}
		} else if _, exists := data["max_tokens"]; !exists {
			data["max_tokens"] = maxTokensCap
		}
	}
	isStreaming := false
	if s, ok := data["stream"].(bool); ok {
		isStreaming = s
	}
	out, err := json.Marshal(data)
	return out, isStreaming, err
}

// isAnthropicError returns true if body matches {"type":"error","error":{...}}.
func isAnthropicError(body []byte) bool {
	var probe struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Type == "error" && len(probe.Error) > 0
}

func sendAnthropicError(w io.Writer, httpStatus int, body []byte) {
	fmt.Fprintf(w, "HTTP/1.1 %d Error\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		httpStatus, len(body))
	w.Write(body)
}

func sendError(w io.Writer, code int, status string) {
	body := status
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, status, len(body), body)
}

func deadlineFromNow(d time.Duration) time.Time {
	return time.Now().Add(d)
}

func (p *Proxy) logVerbose(format string, args ...interface{}) {
	if p.verbose {
		log.Printf(format, args...)
	}
}

// isRetryableStatus returns true for HTTP status codes worth retrying.
// 499 = client closed (musistudio's upstream timeout), 500-503 = server errors.
func isRetryableStatus(code int) bool {
	return code == 499 || (code >= 500 && code <= 503)
}

// retryDelay returns the backoff duration for a given attempt (1-based).
// Uses exponential backoff with jitter: base * 2^(attempt-1) + random jitter.
func retryDelay(attempt int) time.Duration {
	base := config.EgressRetryBaseMs
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	jitter := rand.Intn(config.EgressRetryJitter + 1)
	return time.Duration(base+jitter) * time.Millisecond
}

// isAPIHost returns true for hosts where upstream errors are worth logging.
func isAPIHost(host string) bool {
	return strings.Contains(host, "anthropic.com") ||
		strings.Contains(host, "openai.com") ||
		strings.Contains(host, "localhost") ||
		strings.Contains(host, "127.0.0.1")
}

var bearerRE = regexp.MustCompile(`(?i)bearer\s+\S+`)
var apiKeyRE = regexp.MustCompile(`(?i)(sk-|key-)[a-zA-Z0-9]{8,}`)

// sanitizeForLog redacts Bearer tokens and API key patterns from text.
func sanitizeForLog(s string) string {
	s = bearerRE.ReplaceAllString(s, "Bearer [REDACTED]")
	s = apiKeyRE.ReplaceAllString(s, "$1[REDACTED]")
	return s
}
