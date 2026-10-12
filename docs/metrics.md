# Metrics history and Prometheus

*Added 2026-10-13. Works on every node, with nothing to install. Protocol spec:
[`protocol/WINGS_API.md` §10.3](../protocol/WINGS_API.md#103-metrics-history-and-prometheus).*

Nycto Wings keeps a rolling history of every server's CPU, memory, disk and network usage, so the
panel can draw graphs over hours and days. It also serves the current usage of every server to
Prometheus, for hosting admins' monitoring.

## Why

Upstream Wings only sends live usage over the console websocket while someone has the console
open. Graphs in the panel start empty on every page load, and nobody can see what happened last
night.

## The history

Once a minute, Wings writes one sample for every server that was running during that minute:

| Value | Meaning |
|---|---|
| CPU average and peak | Average and highest CPU use during the minute. 100 is one CPU core |
| Memory average and peak | Average and highest memory use during the minute |
| Disk | Disk space used at the end of the minute |
| Network in and out | Bytes received and sent during the minute |

The averages come from the live usage Docker reports about every second, so a spike that lasted
a few seconds still shows in the peak.

**Storage.** Each server has one file in `/var/lib/nycto/metrics/<server-uuid>.bin` with a fixed
number of slots: 7 days × 1440 minutes = 10,080 slots, about **550 KB per server**. The newest
sample overwrites the oldest, so the file never grows. There's no database, and only the
current minute's figures are kept in memory. The file is deleted along with the server.

Minutes in which a server wasn't running have no sample, so graphs show a gap while it was offline.

### Reading it

The panel asks Wings for the history of a server, and Wings averages it into points for a graph:

```
GET /api/servers/<uuid>/metrics?range=24h&points=300
```

| Parameter | Default | Meaning |
|---|---|---|
| `range` | `24h` | How far back to go: `90m`, `6h`, `24h`, `7d` and so on |
| `from`, `to` | `to` is now | Exact start and end as Unix timestamps, instead of `range` |
| `points` | 300 | Most points to return, 1 to 1000 |

```json
{
  "interval": 60,
  "from": 1791756000,
  "to": 1791842400,
  "points": [
    {
      "time": 1791756000, "span": 300,
      "cpu_avg": 12.5, "cpu_max": 87.25,
      "memory_avg_bytes": 1073741824, "memory_max_bytes": 1610612736,
      "disk_bytes": 5368709120,
      "network_rx_bytes": 104857600, "network_tx_bytes": 52428800
    }
  ]
}
```

Each point covers `span` seconds from `time`. CPU and memory are averaged over the span, the peaks
are the highest in the span, disk is the last value, and network traffic is added up. Divide it by
`span` to get bytes per second. The request needs the node token, like every request from the panel.

## Prometheus

[Prometheus](https://prometheus.io) is the monitoring system most hosting companies use, usually
with Grafana for dashboards and alerts. Wings serves the current usage of every server at
`/metrics` in the Prometheus format:

| Metric | Meaning |
|---|---|
| `nycto_wings_info{version}` | Always 1, with the Wings version |
| `nycto_wings_servers` | Number of servers on the node |
| `nycto_server_state{state}` | 1 for the current state: `offline`, `starting`, `running`, `stopping` |
| `nycto_server_health{status}` | 1 for the current [health](health-checks.md): `offline`, `starting`, `healthy`, `unhealthy` |
| `nycto_server_cpu_percent` | CPU use, 100 is one core |
| `nycto_server_memory_bytes`, `nycto_server_memory_limit_bytes` | Memory used, and the container's limit including overhead |
| `nycto_server_disk_bytes`, `nycto_server_disk_limit_bytes` | Disk used, and the limit (0 is unlimited) |
| `nycto_server_network_receive_bytes_total`, `nycto_server_network_transmit_bytes_total` | Traffic since the container started |
| `nycto_server_uptime_seconds` | Time since the container started |
| `nycto_server_suspended` | 1 if suspended |
| `nycto_server_last_crash_timestamp_seconds` | When it last [crashed](crash-detection.md), if it has since Wings started |

Every server metric has the labels `server` (its UUID) and `name`.

To collect it, add the node to Prometheus with the node's token, the `token` value in
`/etc/nycto/config.yml`:

```yaml
scrape_configs:
  - job_name: nycto-wings
    scheme: https
    authorization:
      credentials: "<node token>"
    static_configs:
      - targets: ["node.example.com:8081"]
```

**The node token gives full control of the node,** so keep the Prometheus configuration as secret
as the token itself. A separate read-only token for metrics is a possible future improvement.

Example alerts: a node's server stuck unhealthy (`nycto_server_health{status="unhealthy"} == 1`
for 5 minutes), or a server near its disk limit
(`nycto_server_disk_bytes / nycto_server_disk_limit_bytes > 0.9 and nycto_server_disk_limit_bytes > 0`).

## Configuration

In `/etc/nycto/config.yml`, under `system`:

```yaml
system:
  metrics:
    enabled: true      # keep the history
    interval: 60       # seconds per sample, 10 to 3600
    retention: 168     # hours of history, 1 to 8760 (at most 100,000 samples per server)
    prometheus: true   # serve /metrics
```

Changing `interval` or `retention` starts each server's history again from empty. With
`enabled: false`, the history endpoint answers 404. Run `systemctl restart nycto-wings` after
changing it. Game servers keep running.

## Compatibility

Both endpoints are new, and nothing else changes. The stock Pterodactyl panel doesn't use them.
