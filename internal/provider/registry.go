package provider

import (
	"fmt"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

// Spec describes one provider type.
type Spec struct {
	Name  string                                     `json:"name"`  // config value of provider.type
	Label string                                     `json:"label"` // shown in the panel
	Help  string                                     `json:"help"`
	New   func(p config.Provider, c *Client) Adapter `json:"-"`
}

// Registry lists every provider type. Adding a type: write an Adapter, add
// it here and add its name to config.ProviderTypes (a test checks both).
var Registry = []Spec{
	{
		Name: config.TypeOpenAI, Label: "OpenAI-compatible",
		Help: "Chat Completions API ({base_url}/chat/completions). Requests are translated; most providers, gateways and local servers speak it.",
		New:  newOpenAI,
	},
	{
		Name: config.TypeAnthropic, Label: "Anthropic-compatible",
		Help: "Anthropic Messages API ({base_url}/v1/messages). Requests are forwarded with the route's overrides, no translation.",
		New:  newAnthropic,
	},
}

// New returns the adapter for a configured provider.
func New(p config.Provider, c *Client) (Adapter, error) {
	for _, s := range Registry {
		if s.Name == p.Type {
			if s.New == nil {
				return nil, fmt.Errorf("provider type %q has no adapter", p.Type)
			}
			return s.New(p, c), nil
		}
	}
	return nil, fmt.Errorf("unknown provider type %q", p.Type)
}
