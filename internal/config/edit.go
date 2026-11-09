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

// RenameAgent moves an agent's route to a new name.
func (c *Config) RenameAgent(oldName, newName string) {
	if rt, ok := c.Agents[oldName]; ok && oldName != newName {
		delete(c.Agents, oldName)
		c.Agents[newName] = rt
	}
}
