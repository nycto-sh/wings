package server

import (
	"fmt"
	"io"
	"strings"

	"github.com/nycto-sh/wings/environment"
	"github.com/nycto-sh/wings/system"
)

// prometheusFamily is a metric with a value for each server.
type prometheusFamily struct {
	name, kind, help string
	value            func(s *Server, u usageSnapshot) float64
}

// usageSnapshot is the resource usage of a server, without the lock that
// guards it.
type usageSnapshot struct {
	environment.Stats
	Disk int64
}

var serverStates = []string{
	environment.ProcessOfflineState,
	environment.ProcessStartingState,
	environment.ProcessRunningState,
	environment.ProcessStoppingState,
}

var healthStatuses = []HealthStatus{HealthOffline, HealthStarting, HealthHealthy, HealthUnhealthy}

var prometheusFamilies = []prometheusFamily{
	{"nycto_server_cpu_percent", "gauge", "CPU usage of the server, where 100 is one CPU core.",
		func(_ *Server, u usageSnapshot) float64 { return u.CpuAbsolute }},
	{"nycto_server_memory_bytes", "gauge", "Memory used by the server.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.Memory) }},
	{"nycto_server_memory_limit_bytes", "gauge", "Memory the server's container may use, including overhead. 0 while offline.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.MemoryLimit) }},
	{"nycto_server_disk_bytes", "gauge", "Disk space used by the server.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.Disk) }},
	{"nycto_server_disk_limit_bytes", "gauge", "Disk space the server may use. 0 means unlimited.",
		func(s *Server, _ usageSnapshot) float64 { return float64(s.DiskSpace()) }},
	{"nycto_server_network_receive_bytes_total", "counter", "Bytes received by the server since its container started.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.Network.RxBytes) }},
	{"nycto_server_network_transmit_bytes_total", "counter", "Bytes sent by the server since its container started.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.Network.TxBytes) }},
	{"nycto_server_uptime_seconds", "gauge", "Seconds since the server's container started. 0 while offline.",
		func(_ *Server, u usageSnapshot) float64 { return float64(u.Uptime) / 1000 }},
	{"nycto_server_suspended", "gauge", "1 if the server is suspended.",
		func(s *Server, _ usageSnapshot) float64 { return boolFloat(s.IsSuspended()) }},
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// escapeLabel escapes a label value in the Prometheus text format.
func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// WritePrometheus writes the resource usage of the servers in the Prometheus
// text exposition format.
//
// @see docs/metrics.md.
func WritePrometheus(w io.Writer, servers []*Server) {
	fmt.Fprintf(w, "# HELP nycto_wings_info Information about Nycto Wings.\n# TYPE nycto_wings_info gauge\n")
	fmt.Fprintf(w, "nycto_wings_info{version=\"%s\"} 1\n", escapeLabel(system.Version))
	fmt.Fprintf(w, "# HELP nycto_wings_servers Number of servers on this node.\n# TYPE nycto_wings_servers gauge\n")
	fmt.Fprintf(w, "nycto_wings_servers %d\n", len(servers))

	type entry struct {
		s      *Server
		labels string
		usage  usageSnapshot
		state  string
		health HealthStatus
	}
	entries := make([]entry, len(servers))
	for i, s := range servers {
		u := s.Proc()
		entries[i] = entry{
			s:      s,
			labels: fmt.Sprintf(`server="%s",name="%s"`, escapeLabel(s.ID()), escapeLabel(s.Config().Meta.Name)),
			usage:  usageSnapshot{Stats: u.Stats, Disk: u.Disk},
			state:  s.Environment.State(),
			health: s.Health().Status,
		}
	}

	fmt.Fprintf(w, "# HELP nycto_server_state 1 for the current state of the server's process.\n# TYPE nycto_server_state gauge\n")
	for _, e := range entries {
		for _, st := range serverStates {
			fmt.Fprintf(w, "nycto_server_state{%s,state=\"%s\"} %g\n", e.labels, st, boolFloat(e.state == st))
		}
	}
	fmt.Fprintf(w, "# HELP nycto_server_health 1 for the current health of the server.\n# TYPE nycto_server_health gauge\n")
	for _, e := range entries {
		for _, h := range healthStatuses {
			fmt.Fprintf(w, "nycto_server_health{%s,status=\"%s\"} %g\n", e.labels, h, boolFloat(e.health == h))
		}
	}
	for _, f := range prometheusFamilies {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		for _, e := range entries {
			fmt.Fprintf(w, "%s{%s} %g\n", f.name, e.labels, f.value(e.s, e.usage))
		}
	}
	fmt.Fprintf(w, "# HELP nycto_server_last_crash_timestamp_seconds When the server last crashed, since Wings started.\n# TYPE nycto_server_last_crash_timestamp_seconds gauge\n")
	for _, e := range entries {
		if c := e.s.LastCrash(); c != nil {
			fmt.Fprintf(w, "nycto_server_last_crash_timestamp_seconds{%s} %d\n", e.labels, c.CrashedAt.Unix())
		}
	}
}
