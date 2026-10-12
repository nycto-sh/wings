package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/apex/log"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/internal/metrics"
)

// metricsAccumulator collects the resource usage reported for a server during
// the current interval of its history.
//
// @see docs/metrics.md.
type metricsAccumulator struct {
	mu     sync.Mutex
	n      int
	cpuSum float64
	cpuMax float64
	memSum float64
	memMax uint64
	// rx and tx are the network counters of the last stats, and lastRx and
	// lastTx their values when the previous sample was taken.
	rx, tx         uint64
	lastRx, lastTx uint64
	haveLast       bool
}

// recordStats adds the resource usage reported by the environment to the
// current interval.
func (s *Server) recordStats(st environment.Stats) {
	a := &s.metricsAcc
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	a.cpuSum += st.CpuAbsolute
	a.cpuMax = max(a.cpuMax, st.CpuAbsolute)
	a.memSum += float64(st.Memory)
	a.memMax = max(a.memMax, st.Memory)
	a.rx, a.tx = st.Network.RxBytes, st.Network.TxBytes
}

// takeSample ends the current interval, which started at start, and returns
// its sample. It returns false if no resource usage was reported during it.
func (s *Server) takeSample(start time.Time) (metrics.Sample, bool) {
	a := &s.metricsAcc
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.n == 0 {
		return metrics.Sample{}, false
	}
	sample := metrics.Sample{
		Time:      start.Unix(),
		CPUAvg:    float32(a.cpuSum / float64(a.n)),
		CPUMax:    float32(a.cpuMax),
		MemoryAvg: uint64(a.memSum / float64(a.n)),
		MemoryMax: a.memMax,
		Disk:      uint64(max(s.Filesystem().CachedUsage(), 0)),
	}
	// The network counters count from when the container started, so they go
	// back to zero when it restarts.
	if a.haveLast {
		sample.NetworkRx = counterDelta(a.rx, a.lastRx)
		sample.NetworkTx = counterDelta(a.tx, a.lastTx)
	}
	a.lastRx, a.lastTx, a.haveLast = a.rx, a.tx, true
	a.n, a.cpuSum, a.cpuMax, a.memSum, a.memMax = 0, 0, 0, 0, 0
	return sample, true
}

// resetNetworkCounters is called when the server stops. The network counters
// of its next run start from zero, so all of its traffic is counted from then.
// What was collected before the stop is still written at the end of the
// interval.
func (s *Server) resetNetworkCounters() {
	a := &s.metricsAcc
	a.mu.Lock()
	a.rx, a.tx, a.lastRx, a.lastTx, a.haveLast = 0, 0, 0, 0, true
	a.mu.Unlock()
}

func counterDelta(cur, last uint64) uint64 {
	if cur < last {
		return cur
	}
	return cur - last
}

// metricsDirectory is where the history files are kept.
func metricsDirectory() string {
	return filepath.Join(config.Get().System.RootDirectory, "metrics")
}

// MetricsHistory returns the server's history file.
func (s *Server) MetricsHistory() *metrics.Ring {
	interval, samples := config.Get().System.Metrics.MetricsSettings()
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metricsRing == nil {
		s.metricsRing = metrics.New(filepath.Join(metricsDirectory(), s.ID()+".bin"), interval, samples)
	}
	return s.metricsRing
}

// RemoveMetricsHistory deletes the server's history file.
func (s *Server) RemoveMetricsHistory() {
	if err := s.MetricsHistory().Remove(); err != nil {
		s.Log().WithField("error", err).Warn("failed to remove the metrics history of the server")
	}
}

// RunMetricsHistory takes a sample of every running server at the end of every
// interval, and adds it to its history, until ctx is canceled. It does nothing
// if the history is turned off.
func (m *Manager) RunMetricsHistory(ctx context.Context) {
	cfg := config.Get().System.Metrics
	if !cfg.Enabled {
		return
	}
	interval, _ := cfg.MetricsSettings()
	if err := os.MkdirAll(metricsDirectory(), 0o700); err != nil {
		log.WithField("error", err).Error("metrics: failed to create the metrics directory")
		return
	}

	every := time.Duration(interval) * time.Second
	for {
		// Samples cover whole intervals of the clock, such as whole minutes.
		start := time.Now().Truncate(every)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(start.Add(every))):
		}
		for _, s := range m.All() {
			sample, ok := s.takeSample(start)
			if !ok {
				continue
			}
			if err := s.MetricsHistory().Write(sample); err != nil {
				s.Log().WithField("error", err).Warn("failed to write the metrics history of the server")
			}
		}
	}
}
