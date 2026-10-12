package config

import "strings"

// HealthCheckConfiguration is a server's health check. The Panel can send one
// as the "health_check" object in a server's settings, and the node operator can
// assign one in the "health_checks" section of the configuration file. Zero
// values use the defaults.
//
// @see docs/health-checks.md and protocol/WINGS_API.md §10.1.
type HealthCheckConfiguration struct {
	// Type is one of "log", "tcp", "http" or "process". Anything else is treated
	// as "log".
	Type string `json:"type" yaml:"type"`
	// Port is the port checked by "tcp" and "http" checks. It must be one of the
	// server's allocations, and defaults to the default allocation.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`
	// Path is requested by "http" checks, and defaults to "/".
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// ExpectedStatus is the status code an "http" check expects. When it is not
	// set, any status from 200 to 399 is healthy.
	ExpectedStatus int `json:"expected_status,omitempty" yaml:"expected_status,omitempty"`
	// Interval is the number of seconds between checks once a server is healthy.
	Interval int `json:"interval,omitempty" yaml:"interval,omitempty"`
	// Timeout is the number of seconds a single check may take.
	Timeout int `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	// StartPeriod is the number of seconds a server has to become healthy after
	// it starts, after which it is marked unhealthy.
	StartPeriod int `json:"start_period,omitempty" yaml:"start_period,omitempty"`
	// Retries is the number of checks in a row that must fail before a healthy
	// server is marked unhealthy.
	Retries int `json:"retries,omitempty" yaml:"retries,omitempty"`
	// RestartOnUnhealthy restarts a server that becomes unhealthy after having
	// been healthy.
	RestartOnUnhealthy bool `json:"restart_on_unhealthy,omitempty" yaml:"restart_on_unhealthy,omitempty"`
}

// HealthCheckOverrides assigns health checks to servers from the node's
// configuration, for panels that do not send them, such as the stock
// Pterodactyl panel. A check sent by the Panel always takes priority.
type HealthCheckOverrides struct {
	// Eggs assigns a health check to every server of an egg, by the egg's UUID.
	Eggs map[string]*HealthCheckConfiguration `json:"-" yaml:"eggs,omitempty"`
	// Servers assigns a health check to a single server, by the server's UUID.
	// It takes priority over the check of the server's egg.
	Servers map[string]*HealthCheckConfiguration `json:"-" yaml:"servers,omitempty"`
}

// Lookup returns the health check assigned to the server, or to its egg, or nil
// if neither has one. UUIDs are compared without regard to case.
func (o HealthCheckOverrides) Lookup(server, egg string) *HealthCheckConfiguration {
	if c := lookupHealthCheck(o.Servers, server); c != nil {
		return c
	}
	return lookupHealthCheck(o.Eggs, egg)
}

func lookupHealthCheck(m map[string]*HealthCheckConfiguration, key string) *HealthCheckConfiguration {
	if key == "" {
		return nil
	}
	if c, ok := m[key]; ok {
		return c
	}
	for k, c := range m {
		if strings.EqualFold(k, key) {
			return c
		}
	}
	return nil
}

// HealthCheckOverride returns the health check the node's configuration assigns
// to the server, or nil. The result must not be modified.
func HealthCheckOverride(server, egg string) *HealthCheckConfiguration {
	mu.RLock()
	defer mu.RUnlock()
	if _config == nil {
		return nil
	}
	return _config.HealthChecks.Lookup(server, egg)
}
