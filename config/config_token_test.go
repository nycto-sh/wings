package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRemoteToken(t *testing.T) {
	t.Setenv("WINGS_TOKEN_ID", "")
	t.Setenv("WINGS_TOKEN", "")

	cfg := Configuration{
		AuthenticationTokenId: "panel-id",
		AuthenticationToken:   "panel-token",
	}
	if err := cfg.ResolveToken(true); err != nil {
		t.Fatalf("expected remote credentials to resolve: %v", err)
	}
	if cfg.Token.ID != "panel-id" || cfg.Token.Token != "panel-token" {
		t.Fatalf("unexpected resolved credentials: %#v", cfg.Token)
	}
}

func TestResolveRemoteTokenRejectsIndirection(t *testing.T) {
	t.Setenv("WINGS_TOKEN_ID", "")
	t.Setenv("WINGS_TOKEN", "")

	tests := []Configuration{
		{AuthenticationTokenId: "file:///tmp/id", AuthenticationToken: "panel-token"},
		{AuthenticationTokenId: "panel-id", AuthenticationToken: "$PANEL_TOKEN"},
	}
	for _, cfg := range tests {
		if err := cfg.ResolveToken(true); err == nil {
			t.Fatal("expected remote token indirection to be rejected")
		}
	}
}

func TestResolveRemoteTokenRequiresEnvironmentMatch(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("panel-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGS_TOKEN_ID", "panel-id")
	t.Setenv("WINGS_TOKEN", "file://"+secret)

	cfg := Configuration{
		AuthenticationTokenId: "panel-id",
		AuthenticationToken:   "panel-token",
	}
	if err := cfg.ResolveToken(true); err != nil {
		t.Fatalf("expected matching environment credentials to resolve: %v", err)
	}

	cfg.AuthenticationToken = "rotated-token"
	if err := cfg.ResolveToken(true); err == nil {
		t.Fatal("expected mismatched environment credentials to be rejected")
	}
}

func TestGetenvPrefersNyctoVariables(t *testing.T) {
	t.Setenv("NYCTO_TOKEN", "nycto-token")
	t.Setenv("WINGS_TOKEN", "wings-token")
	if v := Getenv("TOKEN"); v != "nycto-token" {
		t.Fatalf("expected the NYCTO_ variable to be used, got %q", v)
	}

	t.Setenv("NYCTO_TOKEN", "")
	if v := Getenv("TOKEN"); v != "wings-token" {
		t.Fatalf("expected the WINGS_ variable to be used as a fallback, got %q", v)
	}
}

func TestResolveLocalTokenFromNyctoEnvironment(t *testing.T) {
	t.Setenv("NYCTO_TOKEN_ID", "env-id")
	t.Setenv("NYCTO_TOKEN", "env-token")
	t.Setenv("WINGS_TOKEN_ID", "wings-id")
	t.Setenv("WINGS_TOKEN", "wings-token")

	cfg := Configuration{
		AuthenticationTokenId: "config-id",
		AuthenticationToken:   "config-token",
	}
	if err := cfg.ResolveToken(false); err != nil {
		t.Fatalf("expected local credentials to resolve: %v", err)
	}
	if cfg.Token.ID != "env-id" || cfg.Token.Token != "env-token" {
		t.Fatalf("unexpected resolved credentials: %#v", cfg.Token)
	}
}
