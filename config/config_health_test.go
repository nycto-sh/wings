package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthChecksFromConfigurationFile(t *testing.T) {
	const egg = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const server = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(`health_checks:
  eggs:
    `+egg+`: {type: tcp, start_period: 120}
  servers:
    `+server+`:
      type: http
      port: 8080
      path: /health
      expected_status: 204
      restart_on_unhealthy: true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	c := cfg.HealthChecks.Lookup(server, egg)
	if c == nil || c.Type != "http" || c.Port != 8080 || c.Path != "/health" || c.ExpectedStatus != 204 || !c.RestartOnUnhealthy {
		t.Fatalf("unexpected server health check: %+v", c)
	}
	c = cfg.HealthChecks.Lookup("cccccccc-cccc-4ccc-8ccc-cccccccccccc", strings.ToUpper(egg))
	if c == nil || c.Type != "tcp" || c.StartPeriod != 120 {
		t.Fatalf("unexpected egg health check: %+v", c)
	}
	if c := cfg.HealthChecks.Lookup("cccccccc-cccc-4ccc-8ccc-cccccccccccc", ""); c != nil {
		t.Fatalf("expected no health check, got %+v", c)
	}
}

// A configuration without health checks does not gain an empty section when it
// is written back to disk.
func TestEmptyHealthChecksAreNotWritten(t *testing.T) {
	cfg, err := NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteToDisk(cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "health_checks") {
		t.Fatalf("expected no health_checks section, got:\n%s", b)
	}
}
