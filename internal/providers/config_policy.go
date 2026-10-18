package providers

import "errors"

// CheckModelCalls preserves legacy configurations while allowing generated setups
// to remain inspectable without permitting probes, discovery or routed launches.
func (c *Config) CheckModelCalls() error {
	if c.ModelCallsEnabled != nil && !*c.ModelCallsEnabled {
		return errors.New("model calls disabled by config: configure models and spending limits before enabling")
	}
	return nil
}
