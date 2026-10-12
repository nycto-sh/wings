package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/remote"
)

// healthEnvironment is an environment whose process is reachable on localhost.
type healthEnvironment struct {
	environment.ProcessEnvironment
	mu      sync.Mutex
	state   string
	running bool
}

func (e *healthEnvironment) State() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *healthEnvironment) SetState(st string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = st
}

func (e *healthEnvironment) IsRunning(context.Context) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running, nil
}

func (e *healthEnvironment) Address(context.Context) (string, error) {
	return "127.0.0.1", nil
}

func shortenHealthStartingInterval(t *testing.T) {
	t.Helper()
	previous := healthStartingInterval
	healthStartingInterval = 20 * time.Millisecond
	t.Cleanup(func() { healthStartingInterval = previous })
}

// newHealthServer returns a server in the given state, with the health check
// and a default allocation on port.
func newHealthServer(t *testing.T, state string, hc *HealthCheckConfiguration, port int) (*Server, *healthEnvironment) {
	t.Helper()
	s, err := New(nil)
	require.NoError(t, err)
	t.Cleanup(s.CtxCancel)
	env := &healthEnvironment{state: state, running: true}
	s.Environment = env
	s.procConfig = &remote.ProcessConfiguration{}
	s.cfg.HealthCheck = hc
	s.cfg.Allocations.DefaultMapping.Ip = "0.0.0.0"
	s.cfg.Allocations.DefaultMapping.Port = port
	return s, env
}

// listen opens a TCP listener on localhost and returns it with its port.
func listen(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return l, l.Addr().(*net.TCPAddr).Port
}

// unusedPort returns a localhost port that nothing is listening on.
func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func TestHealthCheckDefaults(t *testing.T) {
	var nilConfig *HealthCheckConfiguration
	require.Equal(t, HealthCheckLog, normalizeHealthCheck(nilConfig).typ)
	require.False(t, normalizeHealthCheck(nilConfig).active())

	hc := normalizeHealthCheck(&HealthCheckConfiguration{Type: "tcp"})
	require.Equal(t, HealthCheckTCP, hc.typ)
	require.Equal(t, 10*time.Second, hc.interval)
	require.Equal(t, 5*time.Second, hc.timeout)
	require.Equal(t, 300*time.Second, hc.startPeriod)
	require.Equal(t, 3, hc.retries)
	require.Equal(t, "/", hc.path.Path)

	require.Equal(t, HealthCheckLog, normalizeHealthCheck(&HealthCheckConfiguration{Type: "ping"}).typ)
}

func TestHealthCheckLimits(t *testing.T) {
	hc := normalizeHealthCheck(&HealthCheckConfiguration{
		Type: "http", Interval: 1, Timeout: 999, StartPeriod: 99999, Retries: 99,
		Port: 70000, ExpectedStatus: 42,
	})
	require.Equal(t, 2*time.Second, hc.interval)
	require.Equal(t, hc.interval, hc.timeout, "the timeout may not be longer than the interval")
	require.Equal(t, 3600*time.Second, hc.startPeriod)
	require.Equal(t, 20, hc.retries)
	require.Zero(t, hc.port)
	require.Zero(t, hc.expectedStatus)
}

// The path of an HTTP check can never change the host that is connected to.
func TestHealthCheckPathCannotChangeHost(t *testing.T) {
	for _, p := range []string{"//evil.example/x", "http://evil.example/", "@evil.example/", "evil", "", "%zz"} {
		u := healthPath(p)
		require.Equal(t, "/", u.Path, p)
		require.Empty(t, u.Host, p)
	}

	u := healthPath("/status?full=1")
	require.Equal(t, "/status", u.Path)
	require.Equal(t, "full=1", u.RawQuery)

	target := normalizeHealthCheck(&HealthCheckConfiguration{Type: "http", Path: "@evil.example/"}).path
	require.Equal(t, "/", target.RequestURI())
}

func TestTCPHealthProbe(t *testing.T) {
	_, port := listen(t)
	s, _ := newHealthServer(t, environment.ProcessRunningState, nil, port)

	ok, msg := s.probeHealth(context.Background(), normalizeHealthCheck(&HealthCheckConfiguration{Type: "tcp"}))
	require.True(t, ok, msg)
	require.Equal(t, "port "+strconv.Itoa(port)+" is accepting connections", msg)

	closed := unusedPort(t)
	s.cfg.Allocations.DefaultMapping.Port = closed
	ok, msg = s.probeHealth(context.Background(), normalizeHealthCheck(&HealthCheckConfiguration{Type: "tcp"}))
	require.False(t, ok)
	require.Equal(t, "port "+strconv.Itoa(closed)+" is not accepting connections", msg)
	require.NotContains(t, msg, "127.0.0.1", "internal addresses must not be shown to users")
}

// Checks only connect to the server's own allocations.
func TestHealthProbeRefusesUnallocatedPorts(t *testing.T) {
	_, port := listen(t)
	s, _ := newHealthServer(t, environment.ProcessRunningState, nil, port+1)

	ok, msg := s.probeHealth(context.Background(), normalizeHealthCheck(&HealthCheckConfiguration{Type: "tcp", Port: port}))
	require.False(t, ok)
	require.Equal(t, "port "+strconv.Itoa(port)+" is not allocated to this server", msg)

	s.cfg.Allocations.Mappings = map[string][]int{"0.0.0.0": {port}}
	ok, msg = s.probeHealth(context.Background(), normalizeHealthCheck(&HealthCheckConfiguration{Type: "tcp", Port: port}))
	require.True(t, ok, msg)
}

func TestHTTPHealthProbe(t *testing.T) {
	var mu sync.Mutex
	status := http.StatusOK
	var path string
	setStatus := func(v int) {
		mu.Lock()
		status = v
		mu.Unlock()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.RequestURI()
		st := status
		mu.Unlock()
		if st == http.StatusFound {
			http.Redirect(w, r, "http://evil.example/", st)
			return
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	s, _ := newHealthServer(t, environment.ProcessRunningState, nil, port)

	hc := normalizeHealthCheck(&HealthCheckConfiguration{Type: "http", Path: "/health?deep=1"})
	ok, msg := s.probeHealth(context.Background(), hc)
	require.True(t, ok, msg)
	mu.Lock()
	require.Equal(t, "/health?deep=1", path)
	mu.Unlock()
	require.Equal(t, "GET /health?deep=1 on port "+strconv.Itoa(port)+" returned 200", msg)

	setStatus(http.StatusServiceUnavailable)
	ok, msg = s.probeHealth(context.Background(), hc)
	require.False(t, ok)
	require.True(t, strings.HasSuffix(msg, "returned 503"), msg)

	// Redirects are not followed, and only count as healthy without an expected
	// status.
	setStatus(http.StatusFound)
	ok, _ = s.probeHealth(context.Background(), hc)
	require.True(t, ok)
	ok, msg = s.probeHealth(context.Background(), normalizeHealthCheck(&HealthCheckConfiguration{Type: "http", ExpectedStatus: 200}))
	require.False(t, ok)
	require.True(t, strings.HasSuffix(msg, "returned 302"), msg)
}

func TestProcessHealthProbe(t *testing.T) {
	s, env := newHealthServer(t, environment.ProcessRunningState, nil, 0)
	hc := normalizeHealthCheck(&HealthCheckConfiguration{Type: "process"})

	ok, _ := s.probeHealth(context.Background(), hc)
	require.True(t, ok)

	env.mu.Lock()
	env.running = false
	env.mu.Unlock()
	ok, msg := s.probeHealth(context.Background(), hc)
	require.False(t, ok)
	require.Equal(t, "the server process is not running", msg)
}

// A server with a health check is marked running when it passes the check.
func TestHealthCheckMarksServerRunning(t *testing.T) {
	shortenHealthStartingInterval(t)
	_, port := listen(t)
	s, env := newHealthServer(t, environment.ProcessStartingState, &HealthCheckConfiguration{Type: "tcp"}, port)

	s.onHealthStateChange()
	require.Equal(t, HealthStarting, s.Health().Status)

	require.Eventually(t, func() bool {
		return env.State() == environment.ProcessRunningState && s.Health().Status == HealthHealthy
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, HealthCheckTCP, s.Health().Check)
}

// A server that never passes its check is marked unhealthy once its start
// period ends, and stays starting.
func TestHealthCheckStartPeriod(t *testing.T) {
	shortenHealthStartingInterval(t)
	hc := &HealthCheckConfiguration{Type: "tcp", StartPeriod: 1, Interval: 2}
	s, env := newHealthServer(t, environment.ProcessStartingState, hc, unusedPort(t))

	s.onHealthStateChange()
	require.Eventually(t, func() bool {
		return s.Health().Status == HealthUnhealthy
	}, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, s.Health().Message, "not healthy 1 seconds after starting")
	require.Equal(t, environment.ProcessStartingState, env.State())
}

// A healthy server that fails its check enough times in a row is unhealthy.
func TestHealthCheckBecomesUnhealthy(t *testing.T) {
	shortenHealthStartingInterval(t)
	l, port := listen(t)
	hc := &HealthCheckConfiguration{Type: "tcp", Interval: 2, Retries: 1}
	s, _ := newHealthServer(t, environment.ProcessStartingState, hc, port)

	s.onHealthStateChange()
	require.Eventually(t, func() bool { return s.Health().Status == HealthHealthy }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, l.Close())
	require.Eventually(t, func() bool { return s.Health().Status == HealthUnhealthy }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, s.Health().Failures)
	require.Contains(t, s.Health().Message, "1 checks in a row failed")
}

// A server that has never run is offline.
func TestNewServerIsOffline(t *testing.T) {
	s, _ := newHealthServer(t, environment.ProcessOfflineState, nil, 0)
	h := s.Health()
	require.Equal(t, HealthOffline, h.Status)
	require.Equal(t, HealthCheckLog, h.Check)
	require.False(t, h.Since.IsZero())
}

// Without a health check, the health follows the state of the process.
func TestLogHealthFollowsState(t *testing.T) {
	s, env := newHealthServer(t, environment.ProcessStartingState, nil, 0)

	s.onHealthStateChange()
	require.Equal(t, HealthStarting, s.Health().Status)
	require.Equal(t, HealthCheckLog, s.Health().Check)

	env.SetState(environment.ProcessRunningState)
	s.onHealthStateChange()
	require.Equal(t, HealthHealthy, s.Health().Status)

	env.SetState(environment.ProcessOfflineState)
	s.onHealthStateChange()
	require.Equal(t, HealthOffline, s.Health().Status)
}

// A server with a health check ignores the "done" lines of its egg.
func TestDoneLineIgnoredWithHealthCheck(t *testing.T) {
	s, env := newHealthServer(t, environment.ProcessStartingState, &HealthCheckConfiguration{Type: "tcp"}, 0)
	var m remote.OutputLineMatcher
	require.NoError(t, m.UnmarshalJSON([]byte(`"Done ("`)))
	s.procConfig.Startup.Done = []*remote.OutputLineMatcher{&m}

	s.onConsoleOutput([]byte("Done (3.2s)! For help, type \"help\""))
	require.Equal(t, environment.ProcessStartingState, env.State())

	s.cfg.HealthCheck = nil
	s.onConsoleOutput([]byte("Done (3.2s)! For help, type \"help\""))
	require.Equal(t, environment.ProcessRunningState, env.State())
}

// Giving a running server a check that probes it starts the check without a
// restart.
func TestHealthCheckAddedWhileRunning(t *testing.T) {
	shortenHealthStartingInterval(t)
	_, port := listen(t)
	s, env := newHealthServer(t, environment.ProcessRunningState, nil, port)

	s.onHealthStateChange()
	require.Equal(t, HealthHealthy, s.Health().Status)
	require.Equal(t, HealthCheckLog, s.Health().Check)

	s.cfg.mu.Lock()
	s.cfg.HealthCheck = &HealthCheckConfiguration{Type: "tcp"}
	s.cfg.mu.Unlock()
	s.onHealthConfigChange()

	require.Eventually(t, func() bool {
		h := s.Health()
		return h.Check == HealthCheckTCP && h.Message == "port "+strconv.Itoa(port)+" is accepting connections"
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, environment.ProcessRunningState, env.State())
}

// Results from an earlier run of the server are ignored.
func TestStaleHealthResultsAreIgnored(t *testing.T) {
	s, _ := newHealthServer(t, environment.ProcessStartingState, &HealthCheckConfiguration{Type: "tcp"}, 0)
	s.beginHealthRun()
	stale := s.health.run
	s.beginHealthRun()

	s.setHealth(stale, HealthHealthy, "old", 0)
	require.Equal(t, HealthStarting, s.Health().Status)
}

// Every change of health is published as a "health" event.
func TestHealthChangesArePublished(t *testing.T) {
	s, env := newHealthServer(t, environment.ProcessStartingState, nil, 0)
	ch := make(chan []byte, 8)
	s.Events().On(ch)
	t.Cleanup(func() { s.Events().Off(ch) })

	s.onHealthStateChange()
	env.SetState(environment.ProcessRunningState)
	s.onHealthStateChange()

	var got []string
	timeout := time.After(time.Second)
	for len(got) < 2 {
		select {
		case b := <-ch:
			if strings.Contains(string(b), `"Topic":"health"`) {
				got = append(got, string(b))
			}
		case <-timeout:
			t.Fatalf("expected two health events, got %v", got)
		}
	}
	require.Contains(t, got[0], `"status":"starting"`)
	require.Contains(t, got[1], `"status":"healthy"`)
}

// A check sent by the Panel takes priority over one assigned by the node's
// configuration, and a server's own check over its egg's.
func TestHealthCheckFromNodeConfiguration(t *testing.T) {
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = "test-token"
	cfg.HealthChecks = config.HealthCheckOverrides{
		Eggs:    map[string]*config.HealthCheckConfiguration{"EGG-UUID": {Type: "process"}},
		Servers: map[string]*config.HealthCheckConfiguration{"server-uuid": {Type: "tcp"}},
	}
	config.Set(cfg)
	t.Cleanup(func() {
		cfg.HealthChecks = config.HealthCheckOverrides{}
		config.Set(cfg)
	})

	s, _ := newHealthServer(t, environment.ProcessOfflineState, nil, 0)
	require.Equal(t, HealthCheckLog, s.healthCheck().typ)

	s.cfg.Egg.ID = "egg-uuid"
	require.Equal(t, HealthCheckProcess, s.healthCheck().typ, "the egg's check applies, ignoring case")

	s.cfg.Uuid = "SERVER-UUID"
	require.Equal(t, HealthCheckTCP, s.healthCheck().typ, "the server's check takes priority over its egg's")
	require.Equal(t, HealthCheckTCP, s.Health().Check)

	s.cfg.HealthCheck = &HealthCheckConfiguration{Type: "http"}
	require.Equal(t, HealthCheckHTTP, s.healthCheck().typ, "the Panel's check takes priority")
}
