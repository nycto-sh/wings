package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMetricsTokenConfig(t *testing.T, token string) string {
	t.Helper()
	t.Setenv("NYCTO_TOKEN_ID", "")
	t.Setenv("NYCTO_TOKEN", "")
	t.Setenv("WINGS_TOKEN_ID", "")
	t.Setenv("WINGS_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.yml")
	body := "token_id: node\ntoken: node-token\nsystem:\n  metrics:\n    token: " + token + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMetricsTokenFromFile(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "metrics-token")
	want := strings.Repeat("m", 40)
	if err := os.WriteFile(secret, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := FromFile(writeMetricsTokenConfig(t, "file://"+secret)); err != nil {
		t.Fatal(err)
	}
	if got := Get().System.Metrics.ResolvedToken(); got != want {
		t.Fatalf("expected the metrics token to be read from the file, got %q", got)
	}
	// The file reference, not the token, is what is written back to disk.
	if Get().System.Metrics.Token != "file://"+secret {
		t.Fatalf("expected the configured value to be kept, got %q", Get().System.Metrics.Token)
	}
}

// A metrics token too short to be safe stops Wings from starting, rather than
// being accepted.
func TestShortMetricsTokenIsRefused(t *testing.T) {
	err := FromFile(writeMetricsTokenConfig(t, "short"))
	if err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Fatalf("expected a short metrics token to be refused, got %v", err)
	}
}

func TestNoMetricsToken(t *testing.T) {
	cfg, err := NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.System.Metrics.resolveToken(); err != nil {
		t.Fatal(err)
	}
	if cfg.System.Metrics.ResolvedToken() != "" {
		t.Fatal("expected no metrics token")
	}
}
