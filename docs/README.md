# Nycto Wings documentation

Guides for the features that Nycto Wings adds or changes compared with Pterodactyl Wings.
The full panel ⇄ daemon contract, including everything listed here, is in
[`protocol/WINGS_API.md`](../protocol/WINGS_API.md).

| Feature | Added | Guide |
|---|---|---|
| Health checks: TCP, HTTP and process checks that decide when a server is ready, with live healthy/unhealthy states | 2026-10-13 | [health-checks.md](health-checks.md) |
| Crash explanations and crash-loop protection: why a server crashed, as structured data, and no more endless restart loops | 2026-10-13 | [crash-detection.md](crash-detection.md) |
| Disk usage checking: no counting before starts or while offline, less counting with plenty of room, on every VPS; instant measuring with project quotas where the disk supports them | 2026-10-13 | [disk-usage.md](disk-usage.md) |
| Docker events: `docker stop` / `docker start` run by hand are handled correctly instead of looking like crashes or going unnoticed | 2026-10-13 | [docker-events.md](docker-events.md) |
| Metrics history and Prometheus: a week of CPU, memory, disk and network per server for graphs, and a `/metrics` endpoint for monitoring | 2026-10-13 | [metrics.md](metrics.md) |

## Maintenance

- [Bringing in upstream changes](upstream-sync.md): how fixes from Pterodactyl Wings get into the
  fork (`scripts/upstream-status.sh`, `scripts/fix-module-path.sh`).

## Installation and setup

- Installing and updating: see the [README](../README.md#install) and `scripts/install.sh`.
- Running next to Pterodactyl Wings on the same machine: see the
  [README](../README.md#running-beside-pterodactyl-wings).
