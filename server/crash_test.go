package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/remote"
)

// crashEnvironment is an offline environment whose process exited as described.
type crashEnvironment struct {
	environment.ProcessEnvironment
	details environment.ExitDetails
	logs    []string
}

func (e *crashEnvironment) State() string { return environment.ProcessOfflineState }

func (e *crashEnvironment) ExitDetails(context.Context) (environment.ExitDetails, error) {
	return e.details, nil
}

func (e *crashEnvironment) Readlog(n int) ([]string, error) {
	if len(e.logs) > n {
		return e.logs[len(e.logs)-n:], nil
	}
	return e.logs, nil
}

// refusingClient is a Panel client that fails to sync a server, which stops a
// restart before it reaches the environment.
type refusingClient struct {
	remote.Client
}

func (refusingClient) GetServerConfiguration(context.Context, string) (remote.ServerConfigurationResponse, error) {
	return remote.ServerConfigurationResponse{}, errors.New("panel unavailable")
}

// useCrashConfig sets the node's crash detection configuration for a test.
func useCrashConfig(t *testing.T, set func(*config.CrashDetection)) {
	t.Helper()
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = "test-token"
	set(&cfg.System.CrashDetection)
	config.Set(cfg)
}

func newCrashServer(t *testing.T, details environment.ExitDetails, logs ...string) (*Server, chan []byte) {
	t.Helper()
	s, err := New(refusingClient{})
	require.NoError(t, err)
	t.Cleanup(s.CtxCancel)
	s.Environment = &crashEnvironment{details: details, logs: logs}
	s.cfg.CrashDetectionEnabled = true
	s.cfg.Build.MemoryLimit = 2048

	ch := make(chan []byte, 64)
	s.Events().On(ch)
	t.Cleanup(func() { s.Events().Off(ch) })
	return s, ch
}

// nextCrashEvent returns the next "crash" event published by the server.
func nextCrashEvent(t *testing.T, ch chan []byte) Crash {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case b := <-ch:
			var e struct {
				Topic string
				Data  json.RawMessage
			}
			require.NoError(t, json.Unmarshal(b, &e))
			if e.Topic != CrashEvent {
				continue
			}
			var c Crash
			require.NoError(t, json.Unmarshal(e.Data, &c))
			return c
		case <-timeout:
			t.Fatal("expected a crash event")
		}
	}
}

func TestCrashReason(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	tests := []struct {
		code    uint32
		oom     bool
		limit   int64
		reason  string
		message string
	}{
		{137, true, 2 * gb, CrashOutOfMemory, "The server ran out of memory (2 GB limit) and was stopped."},
		{137, true, 3 * gb / 2, CrashOutOfMemory, "The server ran out of memory (1.5 GB limit) and was stopped."},
		{137, true, gb / 2, CrashOutOfMemory, "The server ran out of memory (512 MB limit) and was stopped."},
		{137, true, 0, CrashOutOfMemory, "The server ran out of memory and was stopped."},
		{0, false, gb, CrashCleanExit, "The server stopped on its own without reporting an error."},
		{137, false, gb, CrashKilled, "The server was killed (exit code 137). This usually means it ran out of memory or was force-stopped."},
		{143, false, gb, CrashTerminated, "The server was stopped by a termination signal (exit code 143)."},
		{139, false, gb, CrashError, "The server crashed with exit code 139 (segmentation fault)."},
		{1, false, gb, CrashError, "The server crashed with exit code 1."},
	}
	for _, tt := range tests {
		reason, message := crashReason(tt.code, tt.oom, tt.limit)
		require.Equal(t, tt.reason, reason)
		require.Equal(t, tt.message, message)
	}
}

func TestCrashDetectionDefaults(t *testing.T) {
	cfg, err := config.NewAtPath(filepath.Join(t.TempDir(), "config.yml"))
	require.NoError(t, err)
	c := cfg.System.CrashDetection
	require.Equal(t, 60, c.Timeout)
	require.Equal(t, 3, c.MaxCrashes)
	require.Equal(t, 600, c.Window)
	require.Equal(t, 25, c.LogLines)
}

func TestFormatCrashWindow(t *testing.T) {
	require.Equal(t, "10 minutes", formatCrashWindow(10*time.Minute))
	require.Equal(t, "1 minute", formatCrashWindow(time.Minute))
	require.Equal(t, "1 hour", formatCrashWindow(time.Hour))
	require.Equal(t, "90 seconds", formatCrashWindow(90*time.Second))
	require.Equal(t, "a row", formatCrashWindow(0))
}

func TestCrashHistoryWindow(t *testing.T) {
	var cd CrashHandler
	now := time.Now()
	require.Equal(t, 1, cd.record(now.Add(-13*time.Minute), 10*time.Minute))
	require.Equal(t, 1, cd.record(now.Add(-2*time.Minute), 10*time.Minute), "crashes more than the window apart are forgotten")
	require.Equal(t, 2, cd.record(now, 10*time.Minute))
	require.Equal(t, 3, cd.record(now, 0), "a window of 0 counts every crash")
}

// A crash is explained to the Panel, and the server is restarted.
func TestCrashIsExplained(t *testing.T) {
	useCrashConfig(t, func(c *config.CrashDetection) { c.LogLines = 2 })
	started := time.Now().Add(-90 * time.Second)
	s, ch := newCrashServer(t, environment.ExitDetails{
		ExitCode: 137, OOMKilled: true, StartedAt: started, FinishedAt: started.Add(80 * time.Second),
	}, "Loading world", "\x1b[31mjava.lang.OutOfMemoryError: Java heap space\x1b[0m", "Killed")

	err := s.handleServerCrash()
	require.ErrorContains(t, err, "failed to start server after crash detection", "the restart is attempted")

	c := nextCrashEvent(t, ch)
	require.Equal(t, CrashOutOfMemory, c.Reason)
	require.Equal(t, "The server ran out of memory (2 GB limit) and was stopped.", c.Message)
	require.Equal(t, uint32(137), c.ExitCode)
	require.True(t, c.OOMKilled)
	require.Equal(t, int64(2048*1024*1024), c.MemoryLimitBytes)
	require.Equal(t, int64(80), c.UptimeSeconds)
	require.Equal(t, []string{"java.lang.OutOfMemoryError: Java heap space", "Killed"}, c.LastLogs)
	require.True(t, c.Restarting)
	require.Empty(t, c.ReasonNotRestarting)
	require.Equal(t, 1, c.CrashesInWindow)

	require.Equal(t, c.Message, s.LastCrash().Message)
	require.Nil(t, s.PendingCrash(), "a server that is being restarted has no pending crash")
}

// After crashing too many times within the window, a server is not restarted
// until someone starts it.
func TestCrashLoopStopsRestarts(t *testing.T) {
	useCrashConfig(t, func(c *config.CrashDetection) {
		c.Timeout = 0
		c.MaxCrashes = 3
		c.Window = 600
	})
	s, ch := newCrashServer(t, environment.ExitDetails{ExitCode: 1})
	now := time.Now()
	s.crasher.record(now.Add(-5*time.Minute), 10*time.Minute)
	s.crasher.record(now.Add(-2*time.Minute), 10*time.Minute)

	require.NoError(t, s.handleServerCrash())
	c := nextCrashEvent(t, ch)
	require.False(t, c.Restarting)
	require.Equal(t, NotRestartingCrashLoop, c.ReasonNotRestarting)
	require.Equal(t, 3, c.CrashesInWindow)
	require.NotNil(t, s.PendingCrash())

	// A start by a user clears the history and the pending crash.
	s.crasher.onStart()
	require.Nil(t, s.PendingCrash())
	require.Equal(t, 1, s.crasher.record(time.Now(), 10*time.Minute))
}

// Restarts that Wings makes by itself keep the crash history.
func TestAutomaticRestartKeepsCrashHistory(t *testing.T) {
	var cd CrashHandler
	cd.record(time.Now(), time.Hour)
	cd.automatic.Store(true)
	cd.onStart()
	require.Equal(t, 2, cd.record(time.Now(), time.Hour))
}

// The existing rule against restarting within the timeout of the last crash
// still applies.
func TestCrashTooSoonAfterLastCrash(t *testing.T) {
	useCrashConfig(t, func(c *config.CrashDetection) { c.Timeout = 60 })
	s, ch := newCrashServer(t, environment.ExitDetails{ExitCode: 1})
	s.crasher.SetLastCrash(time.Now().Add(-10 * time.Second))

	err := s.handleServerCrash()
	require.True(t, IsTooFrequentCrashError(err))
	c := nextCrashEvent(t, ch)
	require.False(t, c.Restarting)
	require.Equal(t, NotRestartingTooFrequent, c.ReasonNotRestarting)
}

// A server with crash detection disabled is not restarted, but its crash is
// still explained.
func TestCrashWithDetectionDisabled(t *testing.T) {
	useCrashConfig(t, func(*config.CrashDetection) {})
	s, ch := newCrashServer(t, environment.ExitDetails{ExitCode: 1})
	s.cfg.CrashDetectionEnabled = false

	require.NoError(t, s.handleServerCrash())
	c := nextCrashEvent(t, ch)
	require.False(t, c.Restarting)
	require.Equal(t, NotRestartingDisabled, c.ReasonNotRestarting)
	require.Equal(t, CrashError, c.Reason)
}

// externallyStoppedEnvironment is a crashEnvironment whose container was
// stopped from outside of Wings.
type externallyStoppedEnvironment struct {
	crashEnvironment
}

func (e *externallyStoppedEnvironment) StoppedExternally() bool { return true }

// A server stopped from outside of Wings is not a crash, and is not restarted.
func TestExternalStopIsNotACrash(t *testing.T) {
	useCrashConfig(t, func(*config.CrashDetection) {})
	s, _ := newCrashServer(t, environment.ExitDetails{ExitCode: 143})
	s.Environment = &externallyStoppedEnvironment{crashEnvironment{details: environment.ExitDetails{ExitCode: 143}}}

	require.NoError(t, s.handleServerCrash())
	require.Nil(t, s.LastCrash())
}

// A clean exit is not a crash when the node is configured that way.
func TestCleanExitIsNotACrash(t *testing.T) {
	useCrashConfig(t, func(c *config.CrashDetection) { c.DetectCleanExitAsCrash = false })
	s, _ := newCrashServer(t, environment.ExitDetails{ExitCode: 0})

	require.NoError(t, s.handleServerCrash())
	require.Nil(t, s.LastCrash())
}

func TestCrashLogsAreLimited(t *testing.T) {
	long := strings.Repeat("é", maxCrashLogLineLength)
	s, _ := newCrashServer(t, environment.ExitDetails{}, "a", long)

	logs := s.crashLogs(500)
	require.Len(t, logs, 2)
	require.LessOrEqual(t, len(logs[1]), maxCrashLogLineLength)
	require.True(t, strings.HasPrefix(long, logs[1]), "lines are cut at a character boundary")

	require.Equal(t, []string{}, s.crashLogs(0))
}
