package config

// SetAgentRoute sets or removes (Anthropic route) an agent's route.
func (c *Config) SetAgentRoute(name string, rt Route) {
	if c.Agents == nil {
		c.Agents = map[string]Route{}
	}
	if rt.IsAnthropic() {
		delete(c.Agents, name)
		return
	}
	c.Agents[name] = rt
}
