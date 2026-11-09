package config

import (
	"strconv"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the config schema version this build writes.
const CurrentVersion = 2

// migrations upgrade a config document one step: migrations[v] turns a
// version-v document into a later version and must set its "version" key.
// Working on the YAML tree keeps unknown keys and lets a migration rename
// or move fields before the typed decode.
var migrations = map[int]func(root *yaml.Node) error{
	// No version key: written by hand or by an early v2 build. Same schema.
	0: func(root *yaml.Node) error { setVersion(root, 2); return nil },
}

// Parse decodes YAML into a validated config, migrating older versions.
func Parse(data []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, invalid("parse config: %v", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		// An empty file (e.g. caught half-written) must not load as "no
		// providers" and then be saved over the real one.
		return nil, invalid("config file is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, invalid("config must be a YAML mapping")
	}
	v, err := version(root)
	if err != nil {
		return nil, err
	}
	if v > CurrentVersion {
		return nil, invalid("config version %d is newer than this claude-hybrid understands (%d); upgrade claude-hybrid", v, CurrentVersion)
	}
	for v < CurrentVersion {
		m, ok := migrations[v]
		if !ok {
			return nil, invalid("config version %d is not supported (expected %d)", v, CurrentVersion)
		}
		if err := m(root); err != nil {
			return nil, invalid("migrate config from version %d: %v", v, err)
		}
		next, err := version(root)
		if err != nil {
			return nil, err
		}
		if next <= v {
			return nil, invalid("migration from version %d did not advance", v)
		}
		v = next
	}
	var c Config
	if err := root.Decode(&c); err != nil {
		return nil, invalid("parse config: %v", err)
	}
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// version reads the top-level "version" key (0 when missing).
func version(root *yaml.Node) (int, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "version" {
			n, err := strconv.Atoi(root.Content[i+1].Value)
			if err != nil {
				return 0, invalid("config version %q is not a number", root.Content[i+1].Value)
			}
			return n, nil
		}
	}
	return 0, nil
}

func setVersion(root *yaml.Node, v int) {
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "version" {
			root.Content[i+1] = val
			return
		}
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"}
	root.Content = append([]*yaml.Node{key, val}, root.Content...)
}
