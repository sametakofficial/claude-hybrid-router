package anthropic

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// Usage is the token usage of one reply. Input is the whole prompt,
// including the parts that were cache reads and cache writes.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int
}

// UsageWriter wraps a ResponseWriter, records the status code and picks the
// token usage out of an Anthropic reply (SSE or JSON) as it passes through.
// It never changes the bytes sent to the client.
type UsageWriter struct {
	http.ResponseWriter
	status int
	sse    bool
	sniffd bool // sse decided
	line   []byte
	body   []byte // first bytes of a JSON body (usage or error text)
	usage  Usage
}

// NewUsageWriter wraps w.
func NewUsageWriter(w http.ResponseWriter) *UsageWriter { return &UsageWriter{ResponseWriter: w} }

func (s *UsageWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *UsageWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if !s.sniffd {
		s.sniffd = true
		ct := s.Header().Get("Content-Type")
		s.sse = strings.HasPrefix(ct, "text/event-stream") ||
			(!strings.Contains(ct, "json") && (bytes.HasPrefix(b, []byte("event:")) || bytes.HasPrefix(b, []byte("data:"))))
	}
	if s.sse {
		s.scanSSE(b)
	} else if len(s.body) < 1<<20 {
		s.body = append(s.body, b...)
	}
	return s.ResponseWriter.Write(b)
}

func (s *UsageWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *UsageWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Status is the status code sent (200 if nothing was written yet).
func (s *UsageWriter) Status() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// Usage is the usage seen so far (complete once the body is written).
func (s *UsageWriter) Usage() Usage {
	if !s.sse && s.Status() == http.StatusOK {
		s.readUsage(s.body)
	}
	return s.usage
}

// ErrText is the error message of a JSON error body, or the start of the body.
func (s *UsageWriter) ErrText() string {
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

func (s *UsageWriter) scanSSE(b []byte) {
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

type wireUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

func (u wireUsage) input() int { return u.InputTokens + u.CacheRead + u.CacheCreation }

// readUsage handles a full message, a message_start event or a message_delta event.
func (s *UsageWriter) readUsage(data []byte) {
	var v struct {
		Usage   *wireUsage `json:"usage"`
		Message *struct {
			Usage *wireUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &v) != nil {
		return
	}
	if v.Message != nil && v.Message.Usage != nil {
		u := v.Message.Usage
		s.usage = Usage{Input: u.input(), Output: u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}
	}
	if v.Usage != nil {
		if in := v.Usage.input(); in > 0 {
			s.usage.Input = in
			s.usage.CacheRead, s.usage.CacheWrite = v.Usage.CacheRead, v.Usage.CacheCreation
		}
		if v.Usage.OutputTokens > 0 {
			s.usage.Output = v.Usage.OutputTokens
		}
	}
}
