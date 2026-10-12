package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/server/filesystem"
)

// newMetricsServer returns a server with a filesystem, whose environment is in
// the given state.
func newMetricsServer(t *testing.T, state string) *Server {
	t.Helper()
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = "test-token"
	cfg.System.RootDirectory = t.TempDir()
	config.Set(cfg)

	s, err := New(nil)
	require.NoError(t, err)
	t.Cleanup(s.CtxCancel)
	s.cfg.Uuid = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s.cfg.Meta.Name = `My "best" server`
	s.cfg.Build.DiskSpace = 1024
	s.Environment = &healthEnvironment{state: state, running: true}
	s.fs, err = filesystem.New(filepath.Join(t.TempDir(), "data"), s.DiskSpace(), nil)
	require.NoError(t, err)
	return s
}

func stats(cpu float64, mem, rx, tx uint64) environment.Stats {
	return environment.Stats{CpuAbsolute: cpu, Memory: mem, Network: environment.NetworkStats{RxBytes: rx, TxBytes: tx}}
}

func TestMetricsSample(t *testing.T) {
	s := newMetricsServer(t, environment.ProcessRunningState)
	start := time.Unix(1_791_842_400, 0)

	_, ok := s.takeSample(start)
	require.False(t, ok, "no usage was reported")

	s.recordStats(stats(10, 100, 1000, 500))
	s.recordStats(stats(30, 300, 1500, 700))
	sample, ok := s.takeSample(start)
	require.True(t, ok)
	require.Equal(t, int64(1_791_842_400), sample.Time)
	require.Equal(t, float32(20), sample.CPUAvg)
	require.Equal(t, float32(30), sample.CPUMax)
	require.Equal(t, uint64(200), sample.MemoryAvg)
	require.Equal(t, uint64(300), sample.MemoryMax)
	require.Zero(t, sample.NetworkRx, "the traffic before Wings started watching is not known")

	// The next sample has the traffic since this one.
	s.recordStats(stats(5, 100, 2500, 900))
	sample, ok = s.takeSample(start.Add(time.Minute))
	require.True(t, ok)
	require.Equal(t, uint64(1000), sample.NetworkRx)
	require.Equal(t, uint64(200), sample.NetworkTx)
	require.Equal(t, float32(5), sample.CPUMax, "each sample only covers its own interval")

	// After the server stops, the traffic of its next run is counted from zero.
	s.resetNetworkCounters()
	s.recordStats(stats(5, 100, 400, 300))
	sample, ok = s.takeSample(start.Add(2 * time.Minute))
	require.True(t, ok)
	require.Equal(t, uint64(400), sample.NetworkRx)
	require.Equal(t, uint64(300), sample.NetworkTx)
}

func TestMetricsHistoryIsWrittenAndRemoved(t *testing.T) {
	s := newMetricsServer(t, environment.ProcessRunningState)
	s.recordStats(stats(10, 100, 0, 0))
	sample, ok := s.takeSample(time.Unix(1_791_842_400, 0))
	require.True(t, ok)
	require.NoError(t, s.MetricsHistory().Write(sample))

	got, err := s.MetricsHistory().Read(0, 2_000_000_000)
	require.NoError(t, err)
	require.Len(t, got, 1)

	s.RemoveMetricsHistory()
	got, err = s.MetricsHistory().Read(0, 2_000_000_000)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWritePrometheus(t *testing.T) {
	s := newMetricsServer(t, environment.ProcessRunningState)
	s.resources.UpdateStats(stats(42.5, 1<<20, 10, 20))

	var b strings.Builder
	WritePrometheus(&b, []*Server{s})
	out := b.String()

	labels := `server="aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",name="My \"best\" server"`
	for _, line := range []string{
		"# TYPE nycto_server_cpu_percent gauge",
		"nycto_wings_servers 1",
		"nycto_server_cpu_percent{" + labels + "} 42.5",
		"nycto_server_memory_bytes{" + labels + "} 1.048576e+06",
		"nycto_server_disk_limit_bytes{" + labels + "} 1.073741824e+09",
		"nycto_server_network_receive_bytes_total{" + labels + "} 10",
		"nycto_server_state{" + labels + `,state="running"} 1`,
		"nycto_server_state{" + labels + `,state="offline"} 0`,
		"nycto_server_health{" + labels + `,status="offline"} 1`,
		"# TYPE nycto_server_network_transmit_bytes_total counter",
	} {
		require.Contains(t, out, line+"\n")
	}
	require.NotContains(t, out, "nycto_server_last_crash_timestamp_seconds{", "the server has not crashed")
}

func TestMetricsSettings(t *testing.T) {
	interval, samples := config.Metrics{Interval: 60, Retention: 168}.MetricsSettings()
	require.Equal(t, int64(60), interval)
	require.Equal(t, int64(10080), samples)

	interval, samples = config.Metrics{Interval: 1, Retention: 100000}.MetricsSettings()
	require.Equal(t, int64(10), interval)
	require.Equal(t, int64(100000), samples, "the history is limited to 100000 samples")
}
