package config

import (
	"gopkg.in/yaml.v3"
)

// CurrentVersion is the config schema version this build writes.
const CurrentVersion = 2

// Parse decodes YAML into a validated config.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, invalid("parse config: %v", err)
	}
	if c.Version != 0 && c.Version != CurrentVersion {
		return nil, invalid("config version %d is not supported (expected %d)", c.Version, CurrentVersion)
	}
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}
