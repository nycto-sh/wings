package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
)

const (
	testNodeToken    = "node-token-node-token-node-token-node"
	testMetricsToken = "metrics-token-metrics-token-metrics-token"
)

// loadMetricsTestConfig loads a configuration file with a node token and a
// metrics token, the way Wings does when it starts.
func loadMetricsTestConfig(t *testing.T) {
	t.Helper()
	for _, name := range []string{"NYCTO_TOKEN_ID", "NYCTO_TOKEN", "WINGS_TOKEN_ID", "WINGS_TOKEN"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`token_id: node
token: `+testNodeToken+`
system:
  metrics:
    token: `+testMetricsToken+`
`), 0o600))
	require.NoError(t, config.FromFile(path))
}

func newMetricsTestEngine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	engine.GET("/metrics", RequireMetricsAuthorization(), ok)
	engine.GET("/api/system", RequireAuthorization(), ok)
	return engine
}

func requestWithToken(engine *gin.Engine, path, token string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Code
}

// The metrics token only gives access to the metrics.
func TestMetricsTokenOnlyAllowsMetrics(t *testing.T) {
	loadMetricsTestConfig(t)
	engine := newMetricsTestEngine()

	require.Equal(t, http.StatusOK, requestWithToken(engine, "/metrics", testMetricsToken))
	require.Equal(t, http.StatusOK, requestWithToken(engine, "/metrics", testNodeToken), "the node token works too")
	require.Equal(t, http.StatusForbidden, requestWithToken(engine, "/api/system", testMetricsToken), "the metrics token must not work elsewhere")
	require.Equal(t, http.StatusOK, requestWithToken(engine, "/api/system", testNodeToken))

	require.Equal(t, http.StatusForbidden, requestWithToken(engine, "/metrics", "wrong-token"))
	require.Equal(t, http.StatusForbidden, requestWithToken(engine, "/metrics", " "))
	require.Equal(t, http.StatusUnauthorized, requestWithToken(engine, "/metrics", ""))
}

// Without a metrics token, only the node token is accepted.
func TestMetricsWithoutMetricsToken(t *testing.T) {
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = testNodeToken
	config.Set(cfg)
	engine := newMetricsTestEngine()

	require.Equal(t, http.StatusOK, requestWithToken(engine, "/metrics", testNodeToken))
	require.Equal(t, http.StatusForbidden, requestWithToken(engine, "/metrics", testMetricsToken))
}
