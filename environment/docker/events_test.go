package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/events"

	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/system"
)

func killEvent(signal string) events.Message {
	return events.Message{
		Type:   events.ContainerEventType,
		Action: events.ActionKill,
		Actor:  events.Actor{Attributes: map[string]string{"signal": signal}},
	}
}

func TestIsStopSignal(t *testing.T) {
	for _, s := range []string{"2", "3", "9", "15", "SIGTERM", "TERM", "sigkill", "SIGINT"} {
		if !isStopSignal(s) {
			t.Errorf("expected %q to be a stop signal", s)
		}
	}
	for _, s := range []string{"1", "10", "HUP", "SIGUSR1", "", "abc"} {
		if isStopSignal(s) {
			t.Errorf("expected %q not to be a stop signal", s)
		}
	}
}

// A stop signal sent while the server is running came from outside of Wings.
func TestStopSignalFromOutsideWings(t *testing.T) {
	for _, st := range []string{environment.ProcessRunningState, environment.ProcessStartingState} {
		e := &Environment{meta: &Metadata{}, st: system.NewAtomicString(st)}
		e.OnContainerEvent(killEvent("15"))
		if !e.StoppedExternally() {
			t.Fatalf("expected a stop signal while %s to count as a stop from outside of Wings", st)
		}
		if e.StoppedExternally() {
			t.Fatal("expected the stop to be forgotten once it has been reported")
		}
	}
}

// Wings marks the server as stopping before it signals the container itself.
func TestStopSignalFromWings(t *testing.T) {
	e := &Environment{meta: &Metadata{}, st: system.NewAtomicString(environment.ProcessStoppingState)}
	e.OnContainerEvent(killEvent("9"))
	if e.StoppedExternally() {
		t.Fatal("expected a signal sent while stopping not to count as a stop from outside of Wings")
	}
}

// Signals that do not normally stop a program, such as SIGHUP, are ignored.
func TestReloadSignalIsNotAStop(t *testing.T) {
	e := &Environment{meta: &Metadata{}, st: system.NewAtomicString(environment.ProcessRunningState)}
	e.OnContainerEvent(killEvent("1"))
	if e.StoppedExternally() {
		t.Fatal("expected SIGHUP not to count as a stop")
	}
}

// A stop signal long before the container stopped is not what stopped it.
func TestOldStopSignalIsIgnored(t *testing.T) {
	e := &Environment{meta: &Metadata{}, st: system.NewAtomicString(environment.ProcessRunningState)}
	e.externalStop.Store(time.Now().Add(-externalStopWindow - time.Minute).UnixNano())
	if e.StoppedExternally() {
		t.Fatal("expected an old stop signal to be ignored")
	}
}
