package docker

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/apex/log"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

	"github.com/nycto-sh/wings/environment"
)

// Docker's event stream lets Wings notice what is done to a server's container
// outside of Wings, such as "docker stop" or "docker start" run by hand.
//
// @see docs/docker-events.md.

// externalStopWindow is how long after a stop signal sent from outside Wings
// the container stopping is put down to that signal.
const externalStopWindow = 5 * time.Minute

// WatchEvents follows Docker's event stream for server containers until ctx is
// canceled, calling handle with the name of the container and the event. It
// connects again when the stream ends, for example when Docker restarts.
func WatchEvents(ctx context.Context, handle func(name string, msg events.Message)) {
	cli, err := environment.Docker()
	if err != nil {
		log.WithField("error", err).Error("docker events: could not create client")
		return
	}

	backoff := time.Second
	for ctx.Err() == nil {
		args := filters.NewArgs(
			filters.Arg("type", string(events.ContainerEventType)),
			filters.Arg("label", "ContainerType=server_process"),
			filters.Arg("event", string(events.ActionStart)),
			filters.Arg("event", string(events.ActionKill)),
		)
		connected := time.Now()
		msgs, errs := cli.Events(ctx, events.ListOptions{Filters: args})
		log.Debug("docker events: following the event stream")

	stream:
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgs:
				handle(msg.Actor.Attributes["name"], msg)
			case err := <-errs:
				if ctx.Err() == nil {
					log.WithField("error", err).Warn("docker events: lost the event stream, connecting again")
				}
				break stream
			}
		}

		// Start over with a short wait once the stream has been working for a
		// while, and wait longer each time it fails straight away.
		if time.Since(connected) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// OnContainerEvent reacts to a Docker event for the server's container.
func (e *Environment) OnContainerEvent(msg events.Message) {
	switch msg.Action {
	case events.ActionKill:
		// Wings marks a server as stopping before it stops or signals the
		// container itself, so a stop signal sent while the server is running
		// was sent from outside of Wings.
		st := e.State()
		if (st == environment.ProcessRunningState || st == environment.ProcessStartingState) &&
			isStopSignal(msg.Actor.Attributes["signal"]) {
			e.externalStop.Store(time.Now().UnixNano())
			e.log().WithField("signal", msg.Actor.Attributes["signal"]).Warn("server container was sent a stop signal from outside of Wings")
		}
	case events.ActionStart:
		if e.State() == environment.ProcessOfflineState && !e.IsAttached() {
			go e.adoptExternalStart()
		}
	}
}

// isStopSignal reports whether the signal of a "kill" event, given as a number
// or a name, normally stops a program. Signals such as SIGHUP are often used to
// reload a program instead.
func isStopSignal(signal string) bool {
	switch strings.TrimPrefix(strings.ToUpper(signal), "SIG") {
	case "INT", "QUIT", "KILL", "TERM":
		return true
	}
	n, err := strconv.Atoi(signal)
	if err != nil {
		return false
	}
	switch syscall.Signal(n) {
	case syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM:
		return true
	}
	return false
}

// StoppedExternally reports whether the container's last stop followed a stop
// signal sent from outside of Wings, and forgets it.
func (e *Environment) StoppedExternally() bool {
	t := e.externalStop.Swap(0)
	return t != 0 && time.Since(time.Unix(0, t)) < externalStopWindow
}

// adoptExternalStart attaches to a container that was started from outside of
// Wings, so that its console works and it is shown as running.
func (e *Environment) adoptExternalStart() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := e.ContainerInspect(ctx)
	if err != nil || c.ContainerJSONBase == nil || c.State == nil || !c.State.Running {
		return
	}
	// Wings started the server itself in the meantime.
	if e.State() != environment.ProcessOfflineState || e.IsAttached() {
		return
	}

	e.log().Warn("server container was started from outside of Wings, attaching to it")
	e.externalStop.Store(0)
	e.SetState(environment.ProcessRunningState)
	e.applyCpuBurst(ctx)
	if err := e.Attach(ctx); err != nil {
		e.log().WithField("error", err).Error("failed to attach to a server container started from outside of Wings")
	}
}
