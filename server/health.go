package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
)

// Health checks decide when a server is ready for players, and notice when a
// running server stops responding. A server without a health check, which is
// every server on a stock Pterodactyl panel, keeps the upstream behaviour of
// waiting for a line of console output.
//
// @see docs/health-checks.md and protocol/WINGS_API.md §10.

// The types of health check a server can use.
const (
	HealthCheckLog     = "log"
	HealthCheckTCP     = "tcp"
	HealthCheckHTTP    = "http"
	HealthCheckProcess = "process"
)

type HealthStatus string

// The health of a server. A server is "offline" whenever its process is not
// running.
const (
	HealthOffline   HealthStatus = "offline"
	HealthStarting  HealthStatus = "starting"
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
)

// Limits and defaults for the values of a health check, in seconds.
const (
	healthDefaultInterval    = 10
	healthMinInterval        = 2
	healthMaxInterval        = 300
	healthDefaultTimeout     = 5
	healthMaxTimeout         = 60
	healthDefaultStartPeriod = 300
	healthMaxStartPeriod     = 3600
	healthDefaultRetries     = 3
	healthMaxRetries         = 20
)

// healthStartingInterval is how often a server is checked while it starts, so
// that it is marked healthy soon after it is ready. Tests shorten it.
var healthStartingInterval = 2 * time.Second

// HealthCheckConfiguration is the optional "health_check" object in the
// settings the Panel sends for a server.
type HealthCheckConfiguration = config.HealthCheckConfiguration

// healthCheck is a health check configuration with its defaults and limits
// applied.
type healthCheck struct {
	typ            string
	port           int
	path           *url.URL
	expectedStatus int
	interval       time.Duration
	timeout        time.Duration
	startPeriod    time.Duration
	retries        int
	restart        bool
}

// active reports whether the check probes the server, rather than waiting for
// its console output.
func (hc healthCheck) active() bool {
	return hc.typ != HealthCheckLog
}

// clamp returns v limited to [lo, hi], or def if v is not set.
func clamp(v, def, lo, hi int) int {
	if v <= 0 {
		v = def
	}
	return min(max(v, lo), hi)
}

func clampSeconds(v, def, lo, hi int) time.Duration {
	return time.Duration(clamp(v, def, lo, hi)) * time.Second
}

// normalizeHealthCheck applies the defaults and limits to a health check
// configuration. A nil configuration is a "log" check.
func normalizeHealthCheck(c *HealthCheckConfiguration) healthCheck {
	hc := healthCheck{typ: HealthCheckLog}
	if c == nil {
		return hc
	}
	switch c.Type {
	case HealthCheckTCP, HealthCheckHTTP, HealthCheckProcess:
		hc.typ = c.Type
	}
	if c.Port > 0 && c.Port <= 65535 {
		hc.port = c.Port
	}
	hc.path = healthPath(c.Path)
	if c.ExpectedStatus >= 100 && c.ExpectedStatus <= 599 {
		hc.expectedStatus = c.ExpectedStatus
	}
	hc.interval = clampSeconds(c.Interval, healthDefaultInterval, healthMinInterval, healthMaxInterval)
	hc.timeout = clampSeconds(c.Timeout, healthDefaultTimeout, 1, healthMaxTimeout)
	hc.timeout = min(hc.timeout, hc.interval)
	hc.startPeriod = clampSeconds(c.StartPeriod, healthDefaultStartPeriod, 0, healthMaxStartPeriod)
	hc.retries = clamp(c.Retries, healthDefaultRetries, 1, healthMaxRetries)
	hc.restart = c.RestartOnUnhealthy
	return hc
}

// healthPath parses the path an HTTP check requests. Only a path and query are
// kept, so that the check can never be pointed at a different host.
func healthPath(p string) *url.URL {
	u, err := url.Parse(p)
	if err != nil || p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") ||
		u.Scheme != "" || u.Host != "" || u.User != nil {
		return &url.URL{Path: "/"}
	}
	return &url.URL{Path: u.Path, RawQuery: u.RawQuery}
}

// Health is the health of a server, as sent to the Panel in the "health"
// websocket event and in the server details.
type Health struct {
	Status HealthStatus `json:"status"`
	// Check is the type of health check the server uses.
	Check string `json:"check"`
	// Message explains the status, for example the result of the last check.
	Message string `json:"message"`
	// Since is when the server entered this status.
	Since time.Time `json:"since"`
	// Failures is the number of checks in a row that have failed since the
	// server was last healthy.
	Failures int `json:"failures"`
}

// healthMonitor tracks the health of a server, and runs its health checks
// while the server process is running.
type healthMonitor struct {
	mu      sync.Mutex
	current Health
	// run identifies the current run of the server process, so that results
	// from the checks of an earlier run are ignored.
	run uint64
	// ctx and cancel belong to the current run, and are nil while the server
	// is not running.
	ctx    context.Context
	cancel context.CancelFunc
	// probing is set once the checks of the current run have been started.
	probing bool
}

// healthCheck returns the server's health check: the one the Panel sent, or
// else the one the node's configuration assigns to the server or its egg.
func (s *Server) healthCheck() healthCheck {
	s.cfg.mu.RLock()
	c, id, egg := s.cfg.HealthCheck, s.cfg.Uuid, s.cfg.Egg.ID
	s.cfg.mu.RUnlock()
	if c == nil {
		c = config.HealthCheckOverride(id, egg)
	}
	return normalizeHealthCheck(c)
}

// Health returns the current health of the server.
func (s *Server) Health() Health {
	s.health.mu.Lock()
	h := s.health.current
	s.health.mu.Unlock()
	h.Check = s.healthCheck().typ
	if h.Since.IsZero() {
		h.Since = time.Now().UTC()
	}
	return h
}

// setHealth updates the health of the given run of the server, and tells
// anyone listening if it changed. It does nothing, and returns false, if run is
// no longer the current run.
func (s *Server) setHealth(run uint64, status HealthStatus, message string, failures int) bool {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if run != s.health.run {
		return false
	}
	s.updateHealthLocked(status, message, failures)
	return true
}

// updateHealthLocked updates the health of the server. The caller must hold
// s.health.mu.
func (s *Server) updateHealthLocked(status HealthStatus, message string, failures int) {
	cur := s.health.current
	if cur.Status == status && cur.Message == message && cur.Failures == failures && !cur.Since.IsZero() {
		return
	}
	if cur.Status != status || cur.Since.IsZero() {
		cur.Since = time.Now().UTC()
		s.Log().WithField("health", status).WithField("message", message).Info("server health changed")
	}
	cur.Status = status
	cur.Message = message
	cur.Failures = failures
	cur.Check = s.healthCheck().typ
	s.health.current = cur
	s.Events().Publish(HealthEvent, cur)
}

// beginHealthRun starts tracking a new run of the server process, which is
// "starting" until it passes its health check.
func (s *Server) beginHealthRun() {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	s.beginHealthRunLocked()
}

func (s *Server) beginHealthRunLocked() {
	s.stopHealthRunLocked()
	s.health.ctx, s.health.cancel = context.WithCancel(s.Context())

	hc := s.healthCheck()
	msg := "waiting for the server to report that it has started"
	if hc.active() {
		msg = "waiting for the first health check to pass"
	}
	s.updateHealthLocked(HealthStarting, msg, 0)
	if hc.active() {
		s.startProbingLocked()
	}
}

// stopHealthRunLocked stops the checks of the current run, and ignores any
// results they are still producing.
func (s *Server) stopHealthRunLocked() {
	if s.health.cancel != nil {
		s.health.cancel()
	}
	s.health.ctx, s.health.cancel = nil, nil
	s.health.probing = false
	s.health.run++
}

func (s *Server) startProbingLocked() {
	s.health.probing = true
	go s.runHealthChecks(s.health.ctx, s.health.run)
}

// onHealthConfigChange starts checking a running server whose health check was
// changed from "log" to one that probes it. Changes to a check that is already
// running are picked up by the check itself.
func (s *Server) onHealthConfigChange() {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if s.health.cancel != nil && !s.health.probing && s.healthCheck().active() {
		s.startProbingLocked()
	}
}

// onHealthStateChange keeps the health of the server in step with the state of
// its process. It looks at the current state rather than the change that
// triggered it, since state changes are handled concurrently.
func (s *Server) onHealthStateChange() {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()

	switch s.Environment.State() {
	case environment.ProcessOfflineState, environment.ProcessStoppingState:
		if s.health.cancel != nil || s.health.current.Status != HealthOffline {
			s.stopHealthRunLocked()
			s.updateHealthLocked(HealthOffline, "the server is not running", 0)
		}
	case environment.ProcessStartingState:
		if s.health.cancel == nil {
			s.beginHealthRunLocked()
		}
	case environment.ProcessRunningState:
		if s.health.cancel == nil {
			s.beginHealthRunLocked()
		}
		// Without a health check, a running server is healthy, which keeps the
		// meaning "running" has always had.
		if !s.healthCheck().active() {
			s.updateHealthLocked(HealthHealthy, "the server reported that it has started", 0)
		}
	}
}

// runHealthChecks checks the server until ctx is canceled, which happens when
// the run ends.
func (s *Server) runHealthChecks(ctx context.Context, run uint64) {
	started := time.Now()
	failures := 0
	healthy := false
	restarted := false

	timer := time.NewTimer(healthStartingInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		// The configuration is read each time, so that changes from the Panel
		// apply without restarting the server.
		hc := s.healthCheck()
		if !hc.active() {
			// The check was removed while the server was running.
			if s.Environment.State() == environment.ProcessRunningState {
				s.setHealth(run, HealthHealthy, "the server reported that it has started", 0)
			}
			timer.Reset(hc.interval)
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, hc.timeout)
		ok, msg := s.probeHealth(cctx, hc)
		cancel()
		if ctx.Err() != nil {
			return
		}

		switch {
		case ok:
			failures = 0
			healthy = true
			// A server with a health check is running once it passes it, rather
			// than when it prints a line of console output. A result from a run
			// that has already ended must not mark the next run as running.
			if s.setHealth(run, HealthHealthy, msg, 0) && s.Environment.State() == environment.ProcessStartingState {
				s.Environment.SetState(environment.ProcessRunningState)
			}
		case !healthy:
			// The server has not been healthy yet during this run.
			if time.Since(started) >= hc.startPeriod {
				s.setHealth(run, HealthUnhealthy,
					fmt.Sprintf("%s (not healthy %d seconds after starting)", msg, int(hc.startPeriod/time.Second)), 0)
			} else {
				s.setHealth(run, HealthStarting, msg, 0)
			}
		default:
			failures++
			if failures < hc.retries {
				s.setHealth(run, HealthHealthy, msg, failures)
				break
			}
			s.setHealth(run, HealthUnhealthy,
				fmt.Sprintf("%s (%d checks in a row failed)", msg, failures), failures)
			if hc.restart && !restarted {
				// Only restart once per run. The restart begins a new run.
				restarted = true
				go s.restartUnhealthy(failures)
			}
		}

		next := hc.interval
		if !healthy && time.Since(started) < hc.startPeriod {
			next = min(healthStartingInterval, hc.interval)
		}
		timer.Reset(next)
	}
}

// restartUnhealthy restarts a server that has become unhealthy.
func (s *Server) restartUnhealthy(failures int) {
	s.PublishConsoleOutputFromDaemon(fmt.Sprintf("Server failed %d health checks in a row, restarting...", failures))
	s.Log().WithField("failures", failures).Warn("restarting server after it became unhealthy")
	if err := s.HandlePowerAction(PowerActionRestart, 30); err != nil {
		s.Log().WithField("error", err).Error("failed to restart unhealthy server")
	}
}
