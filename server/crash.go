package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"emperror.dev/errors"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/environment"
)

// Why a server crashed, as sent to the Panel in the "crash" event.
//
// @see docs/crash-detection.md and protocol/WINGS_API.md §10.2.
const (
	CrashOutOfMemory = "out_of_memory"
	CrashKilled      = "killed"
	CrashTerminated  = "terminated"
	CrashError       = "error"
	CrashCleanExit   = "clean_exit"
)

// Why a crashed server was not restarted.
const (
	NotRestartingDisabled    = "crash_detection_disabled"
	NotRestartingTooFrequent = "too_frequent"
	NotRestartingCrashLoop   = "crash_loop"
)

const (
	// maxCrashLogLines limits the number of lines of console output in a crash.
	maxCrashLogLines = 100
	// maxCrashLogLineLength limits the length of each of those lines, in bytes.
	maxCrashLogLineLength = 1000
)

// Crash describes a crash of a server, so that the Panel can explain it.
type Crash struct {
	// Reason is one of the Crash* constants.
	Reason string `json:"reason"`
	// Message explains the crash in a sentence that can be shown to users.
	Message   string `json:"message"`
	ExitCode  uint32 `json:"exit_code"`
	OOMKilled bool   `json:"oom_killed"`
	// MemoryLimitBytes is the server's memory limit, or 0 if it has none.
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`
	// UptimeSeconds is how long the process ran before crashing, or 0 if not
	// known.
	UptimeSeconds int64     `json:"uptime_seconds"`
	CrashedAt     time.Time `json:"crashed_at"`
	// LastLogs are the last lines of console output before the crash.
	LastLogs []string `json:"last_logs"`
	// Restarting is whether Wings is restarting the server.
	Restarting bool `json:"restarting"`
	// ReasonNotRestarting is one of the NotRestarting* constants when the
	// server is not being restarted.
	ReasonNotRestarting string `json:"reason_not_restarting,omitempty"`
	// CrashesInWindow is the number of crashes within the crash detection
	// window, including this one.
	CrashesInWindow int `json:"crashes_in_window"`
}

type CrashHandler struct {
	mu sync.RWMutex

	// Tracks the time of the last crash after which the server was restarted.
	lastCrash time.Time

	// recent holds the times of the crashes counted for crash loop detection.
	recent []time.Time

	// last is the most recent crash, and pending is set while the server is
	// offline because of it.
	last    *Crash
	pending bool

	// automatic is set while Wings itself restarts the server, so that the
	// restart does not clear the crash history the way a start by a user does.
	automatic atomic.Bool
}

// Returns the time of the last crash for this server instance.
func (cd *CrashHandler) LastCrashTime() time.Time {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	return cd.lastCrash
}

// Sets the last crash time for a server.
func (cd *CrashHandler) SetLastCrash(t time.Time) {
	cd.mu.Lock()
	cd.lastCrash = t
	cd.mu.Unlock()
}

// record counts a crash at t, and returns the number of crashes within window
// of it. A window of 0 counts every crash since the history was last cleared.
func (cd *CrashHandler) record(t time.Time, window time.Duration) int {
	cd.mu.Lock()
	defer cd.mu.Unlock()
	kept := cd.recent[:0]
	for _, c := range cd.recent {
		if window <= 0 || t.Sub(c) < window {
			kept = append(kept, c)
		}
	}
	cd.recent = append(kept, t)
	return len(cd.recent)
}

// setLast stores the most recent crash.
func (cd *CrashHandler) setLast(c *Crash) {
	cd.mu.Lock()
	cd.last = c
	cd.pending = !c.Restarting
	cd.mu.Unlock()
}

// onStart is called whenever the server is started. Unless Wings is restarting
// the server itself, the crash history is cleared, so that a server stopped for
// crashing too often is restarted after crashes again once a user starts it.
func (cd *CrashHandler) onStart() {
	cd.mu.Lock()
	defer cd.mu.Unlock()
	cd.pending = false
	if !cd.automatic.Load() {
		cd.recent = nil
	}
}

// LastCrash returns the most recent crash of the server, or nil if it has not
// crashed since Wings started.
func (s *Server) LastCrash() *Crash {
	s.crasher.mu.RLock()
	defer s.crasher.mu.RUnlock()
	if s.crasher.last == nil {
		return nil
	}
	c := *s.crasher.last
	return &c
}

// PendingCrash returns the crash that left the server offline, or nil if the
// server was started since its last crash or was restarted after it.
func (s *Server) PendingCrash() *Crash {
	s.crasher.mu.RLock()
	pending := s.crasher.pending
	s.crasher.mu.RUnlock()
	if !pending {
		return nil
	}
	return s.LastCrash()
}

// restartAutomatically runs a power action that Wings starts by itself, which
// does not clear the crash history.
func (s *Server) restartAutomatically(action PowerAction, waitSeconds ...int) error {
	s.crasher.automatic.Store(true)
	defer s.crasher.automatic.Store(false)
	return s.HandlePowerAction(action, waitSeconds...)
}

// exitDetailsEnvironment is an environment that can report when its process
// started and stopped, in addition to how it exited.
type exitDetailsEnvironment interface {
	ExitDetails(ctx context.Context) (environment.ExitDetails, error)
}

func (s *Server) exitDetails() (environment.ExitDetails, error) {
	if env, ok := s.Environment.(exitDetailsEnvironment); ok {
		ctx, cancel := context.WithTimeout(s.Context(), 10*time.Second)
		defer cancel()
		return env.ExitDetails(ctx)
	}
	code, oom, err := s.Environment.ExitState()
	return environment.ExitDetails{ExitCode: code, OOMKilled: oom}, err
}

// Looks at the environment exit state to determine if the process exited cleanly or
// if it was the result of an event that we should try to recover from.
//
// This function assumes it is called under circumstances where a crash is suspected
// of occurring. It will not do anything to determine if it was actually a crash, just
// look at the exit state and check if it meets the criteria of being called a crash
// by Wings.
//
// If the server is determined to have crashed, the Panel is told why in a "crash"
// event, and the process is restarted unless crash detection is disabled, it
// crashed too soon after the last crash, or it has crashed too often recently.
func (s *Server) handleServerCrash() error {
	// No point in doing anything here if the server isn't currently offline, there
	// is no reason to do a crash detection event.
	if s.Environment.State() != environment.ProcessOfflineState {
		return nil
	}

	cfg := config.Get().System.CrashDetection
	details, err := s.exitDetails()
	if err != nil {
		return errors.Wrap(err, "failed to get exit state for server process")
	}

	// If the system is not configured to detect a clean exit code as a crash, and the
	// crash is not the result of the program running out of memory, do nothing.
	if details.ExitCode == 0 && !details.OOMKilled && !cfg.DetectCleanExitAsCrash {
		s.Log().Debug("server exited with successful exit code; system is configured to not detect this as a crash")
		return nil
	}

	now := time.Now()
	crash := s.describeCrash(details, now, cfg.LogLines)
	window := time.Duration(max(cfg.Window, 0)) * time.Second
	crash.CrashesInWindow = s.crasher.record(now, window)

	s.PublishConsoleOutputFromDaemon("---------- Detected server process in a crashed state! ----------")
	s.PublishConsoleOutputFromDaemon(fmt.Sprintf("Exit code: %d", details.ExitCode))
	s.PublishConsoleOutputFromDaemon(fmt.Sprintf("Out of memory: %t", details.OOMKilled))
	s.PublishConsoleOutputFromDaemon(crash.Message)

	last := s.crasher.LastCrashTime()
	var result error
	switch {
	case !s.Config().CrashDetectionEnabled:
		crash.ReasonNotRestarting = NotRestartingDisabled
		s.Log().Debug("server triggered crash detection but handler is disabled for server process")
		s.PublishConsoleOutputFromDaemon("Aborting automatic restart, crash detection is disabled for this instance.")
	// If the last crash time was within the last `timeout` seconds we do not want to perform
	// an automatic reboot of the process.
	//
	// If timeout is set to 0, always reboot the server (this is probably a terrible idea, but some people want it)
	case cfg.Timeout != 0 && !last.IsZero() && last.Add(time.Second*time.Duration(cfg.Timeout)).After(now):
		crash.ReasonNotRestarting = NotRestartingTooFrequent
		s.PublishConsoleOutputFromDaemon("Aborting automatic restart, last crash occurred less than " + strconv.Itoa(cfg.Timeout) + " seconds ago.")
		result = &crashTooFrequent{}
	case cfg.MaxCrashes > 0 && crash.CrashesInWindow >= cfg.MaxCrashes:
		crash.ReasonNotRestarting = NotRestartingCrashLoop
		s.PublishConsoleOutputFromDaemon(fmt.Sprintf(
			"Aborting automatic restart, the server crashed %d times in %s. Start it again once the problem is fixed.",
			crash.CrashesInWindow, formatCrashWindow(window)))
		s.Log().WithField("crashes", crash.CrashesInWindow).Info("not restarting server; it is crashing repeatedly")
	default:
		crash.Restarting = true
	}

	s.crasher.setLast(crash)
	s.Events().Publish(CrashEvent, crash)
	if !crash.Restarting {
		return result
	}

	s.crasher.SetLastCrash(now)
	return errors.Wrap(s.restartAutomatically(PowerActionStart), "failed to start server after crash detection")
}

// describeCrash explains how the server process ended.
func (s *Server) describeCrash(d environment.ExitDetails, now time.Time, logLines int) *Crash {
	c := &Crash{
		ExitCode:         d.ExitCode,
		OOMKilled:        d.OOMKilled,
		MemoryLimitBytes: max(s.MemoryLimit(), 0) * 1024 * 1024,
		CrashedAt:        now.UTC(),
		LastLogs:         s.crashLogs(logLines),
	}
	if !d.StartedAt.IsZero() {
		end := d.FinishedAt
		if end.IsZero() || end.Before(d.StartedAt) {
			end = now
		}
		c.UptimeSeconds = int64(end.Sub(d.StartedAt) / time.Second)
	}
	c.Reason, c.Message = crashReason(d.ExitCode, d.OOMKilled, c.MemoryLimitBytes)
	return c
}

// crashSignals names the signals behind exit codes above 128 that a crash
// message mentions.
var crashSignals = map[uint32]string{
	134: "aborted",
	136: "floating point error",
	139: "segmentation fault",
}

// crashReason returns the reason for a crash and a sentence explaining it.
func crashReason(code uint32, oom bool, memoryLimit int64) (string, string) {
	switch {
	case oom:
		if memoryLimit > 0 {
			return CrashOutOfMemory, fmt.Sprintf("The server ran out of memory (%s limit) and was stopped.", formatMemory(memoryLimit))
		}
		return CrashOutOfMemory, "The server ran out of memory and was stopped."
	case code == 0:
		return CrashCleanExit, "The server stopped on its own without reporting an error."
	case code == 137:
		return CrashKilled, "The server was killed (exit code 137). This usually means it ran out of memory or was force-stopped."
	case code == 143:
		return CrashTerminated, "The server was stopped by a termination signal (exit code 143)."
	}
	if name, ok := crashSignals[code]; ok {
		return CrashError, fmt.Sprintf("The server crashed with exit code %d (%s).", code, name)
	}
	return CrashError, fmt.Sprintf("The server crashed with exit code %d.", code)
}

// formatMemory formats a memory limit the way the Panel shows it, for example
// "512 MB" or "2 GB".
func formatMemory(bytes int64) string {
	mib := bytes / (1024 * 1024)
	if mib < 1024 {
		return fmt.Sprintf("%d MB", mib)
	}
	gib := float64(mib) / 1024
	if gib == float64(int64(gib)) {
		return fmt.Sprintf("%d GB", int64(gib))
	}
	return fmt.Sprintf("%.1f GB", gib)
}

// formatCrashWindow formats the crash detection window for the console, for
// example "10 minutes".
func formatCrashWindow(d time.Duration) string {
	switch {
	case d <= 0:
		return "a row"
	case d%time.Hour == 0:
		return pluralize(int(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return pluralize(int(d/time.Minute), "minute")
	}
	return pluralize(int(d/time.Second), "second")
}

func pluralize(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// crashLogs returns the last lines of console output from the server process,
// without colors and with long lines cut short.
func (s *Server) crashLogs(n int) []string {
	out := []string{}
	n = min(n, maxCrashLogLines)
	if n <= 0 {
		return out
	}
	lines, err := s.Environment.Readlog(n)
	if err != nil {
		s.Log().WithField("error", err).Warn("failed to read console output of crashed server")
		return out
	}
	for _, line := range lines {
		line = strings.TrimRight(stripAnsiRegex.ReplaceAllString(line, ""), "\r\n")
		out = append(out, truncateUTF8(line, maxCrashLogLineLength))
	}
	return out
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
