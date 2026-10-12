package docker

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"emperror.dev/errors"
	"github.com/apex/log"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"

	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/events"
	"github.com/nycto-sh/wings/remote"
	"github.com/nycto-sh/wings/system"
)

type Metadata struct {
	Image string
	Stop  remote.ProcessStopConfiguration
}

// Ensure that the Docker environment is always implementing all the methods
// from the base environment interface.
var _ environment.ProcessEnvironment = (*Environment)(nil)

type Environment struct {
	mu sync.RWMutex

	// The public identifier for this environment. In this case it is the Docker container
	// name that will be used for all instances created under it.
	Id string

	// The environment configuration.
	Configuration *environment.Configuration

	meta *Metadata

	// The Docker client being used for this instance.
	client *client.Client

	// Controls the hijacked response stream which exists only when we're attached to
	// the running container instance.
	stream *types.HijackedResponse

	emitter *events.Bus

	logCallbackMx sync.Mutex
	logCallback   func([]byte)

	// Tracks the environment state.
	st *system.AtomicString

	// Tracks how often the attached stream has ended while the container kept
	// running, guarded by mu.
	reattachedAt    time.Time
	reattachedCount int
}

// New creates a new base Docker environment. The ID passed through will be the
// ID that is used to reference the container from here on out. This should be
// unique per-server (we use the UUID by default). The container does not need
// to exist at this point.
func New(id string, m *Metadata, c *environment.Configuration) (*Environment, error) {
	cli, err := environment.Docker()
	if err != nil {
		return nil, err
	}

	e := &Environment{
		Id:            id,
		Configuration: c,
		meta:          m,
		client:        cli,
		st:            system.NewAtomicString(environment.ProcessOfflineState),
		emitter:       events.NewBus(),
	}

	return e, nil
}

func (e *Environment) log() *log.Entry {
	return log.WithField("environment", e.Type()).WithField("container_id", e.Id)
}

func (e *Environment) Type() string {
	return "docker"
}

// SetStream sets the current stream value from the Docker client. If a nil
// value is provided we assume that the stream is no longer operational and the
// instance is effectively offline.
func (e *Environment) SetStream(s *types.HijackedResponse) {
	e.mu.Lock()
	e.stream = s
	e.mu.Unlock()
}

// IsAttached determines if this process is currently attached to the
// container instance by checking if the stream is nil or not.
func (e *Environment) IsAttached() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stream != nil
}

// Events returns an event bus for the environment.
func (e *Environment) Events() *events.Bus {
	return e.emitter
}

// Exists determines if the container exists in this environment. The ID passed
// through should be the server UUID since containers are created utilizing the
// server UUID as the name and docker will work fine when using the container
// name as the lookup parameter in addition to the longer ID auto-assigned when
// the container is created.
func (e *Environment) Exists() (bool, error) {
	_, err := e.ContainerInspect(context.Background())
	if err != nil {
		// If this error is because the container instance wasn't found via Docker we
		// can safely ignore the error and just return false.
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsRunning determines if the server's docker container is currently running.
// If there is no container present, an error will be raised (since this
// shouldn't be a case that ever happens under correctly developed
// circumstances).
//
// You can confirm if the instance wasn't found by using cerrdefs.IsNotFound.
//
// @see docker/client/errors.go
func (e *Environment) IsRunning(ctx context.Context) (bool, error) {
	c, err := e.ContainerInspect(ctx)
	if err != nil {
		return false, err
	}
	return c.State.Running, nil
}

// Address returns the IP address the container can be reached at from Wings.
// This is the container's address on its Docker network, so that connections
// reach the server process directly rather than through the published ports,
// which Docker may answer for even when nothing in the container is listening.
func (e *Environment) Address(ctx context.Context) (string, error) {
	c, err := e.ContainerInspect(ctx)
	if err != nil {
		return "", err
	}
	if c.ContainerJSONBase != nil && c.HostConfig != nil && c.HostConfig.NetworkMode.IsHost() {
		return "127.0.0.1", nil
	}
	if c.NetworkSettings != nil {
		names := make([]string, 0, len(c.NetworkSettings.Networks))
		for name := range c.NetworkSettings.Networks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if ep := c.NetworkSettings.Networks[name]; ep != nil && ep.IPAddress != "" {
				return ep.IPAddress, nil
			}
		}
		for _, name := range names {
			if ep := c.NetworkSettings.Networks[name]; ep != nil && ep.GlobalIPv6Address != "" {
				return ep.GlobalIPv6Address, nil
			}
		}
	}
	return "", errors.New("environment/docker: container has no network address")
}

// ExitState returns the container exit state, the exit code and whether or not
// the container was killed by the OOM killer.
func (e *Environment) ExitState() (uint32, bool, error) {
	c, err := e.ContainerInspect(context.Background())
	if err != nil {
		// I'm not entirely sure how this can happen to be honest. I tried deleting a
		// container _while_ a server was running and wings gracefully saw the crash and
		// created a new container for it.
		//
		// However, someone reported an error in Discord about this scenario happening,
		// so I guess this should prevent it? They didn't tell me how they caused it though
		// so that's a mystery that will have to go unsolved.
		//
		// @see https://github.com/pterodactyl/panel/issues/2003
		if cerrdefs.IsNotFound(err) {
			return 1, false, nil
		}
		return 0, false, errors.WrapIf(err, "environment/docker: failed to inspect container")
	}
	return uint32(c.State.ExitCode), c.State.OOMKilled, nil
}

// ExitDetails returns how the container last exited, including when it started
// and stopped, which the crash handler reports to the Panel.
func (e *Environment) ExitDetails(ctx context.Context) (environment.ExitDetails, error) {
	c, err := e.ContainerInspect(ctx)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			// The same result as ExitState, for a container that has disappeared.
			return environment.ExitDetails{ExitCode: 1}, nil
		}
		return environment.ExitDetails{}, errors.WrapIf(err, "environment/docker: failed to inspect container")
	}
	d := environment.ExitDetails{}
	if c.ContainerJSONBase == nil || c.State == nil {
		return d, nil
	}
	d.ExitCode = uint32(c.State.ExitCode)
	d.OOMKilled = c.State.OOMKilled
	// Docker reports times that are not set as "0001-01-01T00:00:00Z", which parse
	// to the zero time.
	d.StartedAt, _ = time.Parse(time.RFC3339Nano, c.State.StartedAt)
	d.FinishedAt, _ = time.Parse(time.RFC3339Nano, c.State.FinishedAt)
	return d, nil
}

// Config returns the environment configuration allowing a process to make
// modifications of the environment on the fly.
func (e *Environment) Config() *environment.Configuration {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.Configuration
}

// SetStopConfiguration sets the stop configuration for the environment.
func (e *Environment) SetStopConfiguration(c remote.ProcessStopConfiguration) {
	e.mu.Lock()
	e.meta.Stop = c
	e.mu.Unlock()
}

func (e *Environment) SetImage(i string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.meta.Image = i
}

func (e *Environment) State() string {
	return e.st.Load()
}

// SetState sets the state of the environment. This emits an event that server's
// can hook into to take their own actions and track their own state based on
// the environment.
func (e *Environment) SetState(state string) {
	if state != environment.ProcessOfflineState &&
		state != environment.ProcessStartingState &&
		state != environment.ProcessRunningState &&
		state != environment.ProcessStoppingState {
		panic(errors.New(fmt.Sprintf("invalid server state received: %s", state)))
	}

	// Emit the event to any listeners that are currently registered.
	if e.State() != state {
		// If the state changed make sure we update the internal tracking to note that.
		e.st.Store(state)
		e.Events().Publish(environment.StateChangeEvent, state)
	}
}

func (e *Environment) SetLogCallback(f func([]byte)) {
	e.logCallbackMx.Lock()
	defer e.logCallbackMx.Unlock()

	e.logCallback = f
}
