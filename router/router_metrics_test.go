package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/internal/metrics"
	"github.com/nycto-sh/wings/remote"
	wserver "github.com/nycto-sh/wings/server"
)

func TestParseMetricsRange(t *testing.T) {
	for v, want := range map[string]time.Duration{"90m": 90 * time.Minute, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		got, err := parseMetricsRange(v)
		require.NoError(t, err, v)
		require.Equal(t, want, got, v)
	}
	for _, v := range []string{"", "d", "week", "7x"} {
		_, err := parseMetricsRange(v)
		require.Error(t, err, v)
	}
}

func TestServerMetricsHistory(t *testing.T) {
	previous := config.Get()
	t.Cleanup(func() { config.Set(previous) })
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = "test-token"
	cfg.System.Data = t.TempDir()
	cfg.System.RootDirectory = t.TempDir()
	config.Set(cfg)

	const uuid = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	settings, err := json.Marshal(map[string]any{"uuid": uuid})
	require.NoError(t, err)
	s, err := wserver.NewEmptyManager(backupTestRemoteClient{}).InitServer(uuid, remote.ServerConfigurationResponse{Settings: settings, ProcessConfiguration: &remote.ProcessConfiguration{}})
	require.NoError(t, err)
	t.Cleanup(s.CtxCancel)
	s.Environment = backupTestEnvironment{}

	now := time.Now().Truncate(time.Minute).Unix()
	for i := int64(1); i <= 3; i++ {
		require.NoError(t, s.MetricsHistory().Write(metrics.Sample{Time: now - i*60, CPUAvg: float32(i * 10), CPUMax: float32(i * 20)}))
	}

	request := func(query string) (int, map[string]any) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/servers/"+uuid+"/metrics"+query, nil)
		c.Set("server", s)
		c.Set("logger", log.WithField("test", t.Name()))
		getServerMetrics(c)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return w.Code, body
	}

	code, body := request("?range=1h")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, float64(60), body["interval"])
	points := body["points"].([]any)
	require.Len(t, points, 3)
	first := points[0].(map[string]any)
	require.Equal(t, float64(now-180), first["time"])
	require.Equal(t, float64(30), first["cpu_avg"])
	require.Equal(t, float64(60), first["cpu_max"])

	code, body = request("?range=1h&points=1")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, body["points"].([]any), 1, "the samples are summarized into one point")

	for _, q := range []string{"?range=week", "?points=0", "?points=5000", "?from=abc", "?from=200&to=100"} {
		code, _ = request(q)
		require.Equal(t, http.StatusBadRequest, code, q)
	}
}
