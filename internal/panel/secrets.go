package panel

import (
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

const maskedToken = "••••"

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return maskedToken
	}
	return k[:3] + "…" + k[len(k)-4:]
}

type providerView struct {
	config.Provider
	HasAPIKey bool `json:"has_api_key"`
}

func viewProvider(pr config.Provider) providerView {
	v := providerView{Provider: pr, HasAPIKey: pr.Key() != ""}
	v.APIKey = maskKey(pr.APIKey)
	if v.Headers == nil {
		v.Headers = map[string]string{}
	}
	v.Models = nonNil(v.Models)
	return v
}

func maskSettings(s config.Server) config.Server {
	if s.PanelToken != "" {
		s.PanelToken = maskedToken
	}
	return s
}

// keepKey returns the stored key when the client sent back nothing or the mask.
func keepKey(sent, stored string) string {
	if sent == "" || sent == maskKey(stored) {
		return stored
	}
	return sent
}
