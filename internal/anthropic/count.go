package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// imageTokens is what one image or PDF page roughly costs. Counting a
// base64 image as text would make a 1.5 MB screenshot look like 500k tokens.
const imageTokens = 1600

// EstimateTokens guesses the input tokens of a request body: about 4
// characters per token for text, a fixed amount per image or document.
func EstimateTokens(body []byte) int {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return len(body)/4 + 1
	}
	chars, media := 0, 0
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if typ, _ := t["type"].(string); typ == "image" || typ == "document" {
				if src, ok := t["source"].(map[string]any); ok && src["type"] == "base64" {
					media++
					return
				}
			}
			for k, val := range t {
				chars += len(k)
				walk(val)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case string:
			chars += len(t)
		case json.Number:
			chars += len(t)
		}
	}
	walk(v)
	return chars/4 + media*imageTokens + 1
}

// WriteCount answers a count_tokens request.
func WriteCount(w http.ResponseWriter, n int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"input_tokens":%d}`, n)
}
