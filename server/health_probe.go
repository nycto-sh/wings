package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

// addressedEnvironment is an environment that can report the address its
// process is reachable at from Wings, such as the container's IP address.
type addressedEnvironment interface {
	Address(ctx context.Context) (string, error)
}

// healthHTTPClient is used by HTTP health checks. It never uses a proxy, does
// not follow redirects and does not keep connections open, so that every check
// is a fresh request to the server itself.
var healthHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{}).DialContext,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// probeHealth runs the health check once, returning whether it passed and a
// message describing the result that can be shown to the server's users.
func (s *Server) probeHealth(ctx context.Context, hc healthCheck) (bool, string) {
	switch hc.typ {
	case HealthCheckProcess:
		running, err := s.Environment.IsRunning(ctx)
		if err != nil || !running {
			return false, "the server process is not running"
		}
		return true, "the server process is running"
	case HealthCheckTCP:
		addr, port, err := s.healthAddress(ctx, hc.port)
		if err != nil {
			return false, err.Error()
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return false, fmt.Sprintf("port %d %s", port, dialFailure(err, hc))
		}
		_ = conn.Close()
		return true, fmt.Sprintf("port %d is accepting connections", port)
	case HealthCheckHTTP:
		return s.probeHTTP(ctx, hc)
	}
	return false, "unknown health check"
}

func (s *Server) probeHTTP(ctx context.Context, hc healthCheck) (bool, string) {
	addr, port, err := s.healthAddress(ctx, hc.port)
	if err != nil {
		return false, err.Error()
	}
	target := url.URL{Scheme: "http", Host: addr, Path: hc.path.Path, RawQuery: hc.path.RawQuery}
	what := fmt.Sprintf("GET %s on port %d", hc.path.RequestURI(), port)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, what + " could not be sent"
	}
	req.Header.Set("User-Agent", "Nycto Wings health check")
	res, err := healthHTTPClient.Do(req)
	if err != nil {
		return false, what + " " + dialFailure(err, hc)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	_ = res.Body.Close()

	ok := res.StatusCode >= 200 && res.StatusCode < 400
	if hc.expectedStatus != 0 {
		ok = res.StatusCode == hc.expectedStatus
	}
	return ok, fmt.Sprintf("%s returned %d", what, res.StatusCode)
}

// dialFailure describes why a connection failed without including addresses,
// which are internal to the node.
func dialFailure(err error, hc healthCheck) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Sprintf("did not answer within %d seconds", int(hc.timeout.Seconds()))
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return fmt.Sprintf("did not answer within %d seconds", int(hc.timeout.Seconds()))
	}
	return "is not accepting connections"
}

// healthAddress returns the address a TCP or HTTP check connects to: the
// server's own container on one of its allocated ports. A port of 0 is the
// default allocation. Checks never connect anywhere else, so a health check
// cannot be used to reach other machines.
func (s *Server) healthAddress(ctx context.Context, port int) (string, int, error) {
	s.cfg.mu.RLock()
	alloc := s.cfg.Allocations
	s.cfg.mu.RUnlock()

	if port == 0 {
		port = alloc.DefaultMapping.Port
	}
	allocated := port > 0 && port == alloc.DefaultMapping.Port
	for _, ports := range alloc.Mappings {
		for _, p := range ports {
			if p == port {
				allocated = true
			}
		}
	}
	if !allocated {
		return "", port, fmt.Errorf("port %d is not allocated to this server", port)
	}

	env, ok := s.Environment.(addressedEnvironment)
	if !ok {
		return "", port, errors.New("the server environment does not support network health checks")
	}
	host, err := env.Address(ctx)
	if err != nil {
		return "", port, errors.New("the server's network address could not be found")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), port, nil
}
