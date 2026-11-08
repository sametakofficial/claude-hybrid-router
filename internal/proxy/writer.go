package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// usage counts tokens; in is the total input including cache reads/writes.
type usage struct{ in, out, cacheRead, cacheWrite int }

// statusWriter records the status code and picks token usage out of the
// Anthropic response (SSE or JSON) as it passes through. It never changes
// the bytes sent to the client.
type statusWriter struct {
	http.ResponseWriter
	status int
	sse    bool
	sniffd bool // sse decided from the response header
	line   []byte
	body   []byte // first bytes of a JSON body (usage or error text)
	usage  usage
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if !s.sniffd {
		s.sniffd = true
		if strings.HasPrefix(s.Header().Get("Content-Type"), "text/event-stream") {
			s.sse = true
		}
	}
	if s.sse {
		s.scanSSE(b)
	} else if len(s.body) < 1<<20 {
		s.body = append(s.body, b...)
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusWriter) code() int {
	if s.status == 0 {
		return http.StatusOK
	}
	if !s.sse && s.status == http.StatusOK {
		s.readUsage(s.body)
	}
	return s.status
}

func (s *statusWriter) errText() string {
	t := strings.TrimSpace(string(s.body))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(s.body, &e) == nil && e.Error.Message != "" {
		t = e.Error.Message
	}
	if len(t) > 500 {
		t = t[:500] + "…"
	}
	return t
}

func (s *statusWriter) scanSSE(b []byte) {
	s.line = append(s.line, b...)
	for {
		i := bytes.IndexByte(s.line, '\n')
		if i < 0 {
			break
		}
		ln := s.line[:i]
		if bytes.HasPrefix(ln, []byte("data:")) && bytes.Contains(ln, []byte(`"usage"`)) {
			s.readUsage(bytes.TrimSpace(ln[5:]))
		}
		s.line = s.line[i+1:]
	}
	if len(s.line) > 1<<20 { // a single absurd line: stop tracking it
		s.line = nil
	}
}

type anthropicUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) input() int { return u.InputTokens + u.CacheRead + u.CacheCreation }

// readUsage handles a full message, a message_start event or a message_delta event.
func (s *statusWriter) readUsage(data []byte) {
	var v struct {
		Usage   *anthropicUsage `json:"usage"`
		Message *struct {
			Usage *anthropicUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &v) != nil {
		return
	}
	if v.Message != nil && v.Message.Usage != nil {
		u := v.Message.Usage
		s.usage = usage{in: u.input(), out: u.OutputTokens, cacheRead: u.CacheRead, cacheWrite: u.CacheCreation}
	}
	if v.Usage != nil {
		if in := v.Usage.input(); in > 0 {
			s.usage.in = in
			s.usage.cacheRead, s.usage.cacheWrite = v.Usage.CacheRead, v.Usage.CacheCreation
		}
		if v.Usage.OutputTokens > 0 {
			s.usage.out = v.Usage.OutputTokens
		}
	}
}
