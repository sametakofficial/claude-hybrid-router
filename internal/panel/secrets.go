package panel

import (
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

// Secrets (API keys, header values, the panel token) never go to the
// browser in full. The browser gets a mask and may send the mask back; a
// masked or blank value then means "keep what is stored".

const maskedToken = "••••"

// maskKey hides a secret, showing at most its last 4 characters ("…abcd").
// Short values are masked completely.
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) < 12 {
		return maskedToken
	}
	return "…" + k[len(k)-4:]
}

// isMasked reports whether s is a mask we produced (now or in an older
// version, e.g. "sk-…6789"). Real keys never contain these characters.
func isMasked(s string) bool {
	return strings.Contains(s, "…") || strings.Contains(s, "•")
}

type providerView struct {
	config.Provider
	HasAPIKey bool `json:"has_api_key"`
}

func viewProvider(pr config.Provider) providerView {
	v := providerView{Provider: pr, HasAPIKey: pr.Key() != ""}
	v.APIKey = maskKey(pr.APIKey)
	v.Headers = maskHeaders(pr.Headers)
	v.Models = nonNil(v.Models)
	return v
}

func maskHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = maskKey(v)
	}
	return out
}

func maskSettings(s config.Server) config.Server {
	if s.PanelToken != "" {
		s.PanelToken = maskedToken
	}
	return s
}

// unmaskProvider resolves the secrets of a provider sent by the browser.
// stored is the saved provider with the same id (nil for a new provider, or
// when the stored secrets must not be used). A blank or masked api_key keeps
// the stored key, clearKey removes it. Masked header values keep the stored
// value of the same header. Masked values that cannot be resolved are
// dropped (never saved as the literal mask); dropped reports that.
func unmaskProvider(sent config.Provider, stored *config.Provider, clearKey bool) (out config.Provider, dropped bool) {
	out = sent
	switch {
	case clearKey:
		out.APIKey = ""
	case sent.APIKey == "" || isMasked(sent.APIKey):
		out.APIKey = ""
		if stored != nil {
			out.APIKey = stored.APIKey
		} else if sent.APIKey != "" {
			dropped = true
		}
	}
	if len(sent.Headers) == 0 {
		out.Headers = nil
		return out, dropped
	}
	out.Headers = make(map[string]string, len(sent.Headers))
	for k, v := range sent.Headers {
		if !isMasked(v) {
			out.Headers[k] = v
			continue
		}
		if stored != nil {
			if old, ok := stored.Headers[k]; ok {
				out.Headers[k] = old
				continue
			}
		}
		dropped = true
	}
	return out, dropped
}

// unmaskToken keeps the stored panel token when the browser sent the mask or nothing.
func unmaskToken(sent, stored string) string {
	if sent == "" || isMasked(sent) {
		return stored
	}
	return sent
}
