# CLAUDE.md: Nycto Wings

## Project context

- **Nycto Wings** is the node daemon for **Nycto**, a new open-source game/app
  server hosting panel.
- This repo is a fork of [pterodactyl/wings](https://github.com/pterodactyl/wings)
  (MIT). The repo owner maintains it. A partner builds the Nycto panel from
  scratch in a separate repo (Go backend, embedded frontend, SQLite/Postgres,
  AGPL-3.0).
- This daemon stays **MIT**. The original copyright, `LICENSE` and upstream
  attribution must stay intact.
- **For now, the daemon must stay fully compatible with the existing Wings
  protocol**, so it works with the stock Pterodactyl panel *and* the new Nycto
  panel. Any divergence comes later, on purpose, and only with explicit approval.
- `protocol/WINGS_API.md` is the reference for the panel ⇄ daemon contract.
  Update it in the same change whenever protocol behaviour changes.

## Repo layout

| Path | What lives there |
|---|---|
| `wings.go` | `main`. Calls `cmd.Execute()` |
| `cmd/` | Cobra CLI: `root.go` (daemon boot: `rootCmdRun`), `configure.go` (`wings configure`, fetches node config from the panel), `diagnostics.go` |
| `config/` | YAML config singleton (`config.Get/Set/Update`), defaults, token resolution, JWT key, Docker config (`config_docker.go`) |
| `remote/` | HTTP client for the panel's `/api/remote/*` API (`remote.Client`) |
| `router/` | Gin HTTP API that Wings serves. `router.go:Configure` registers every route |
| `router/middleware/` | Node-token auth, CORS, request IDs, error formatting |
| `router/tokens/` | JWT payload types, scope checks, one-time-use store, denylist |
| `router/websocket/` | Console websocket protocol, permissions, rate limits |
| `router/downloader/` | Remote URL → server file downloads (SSRF-guarded) |
| `server/` | Per-server state machine: `Manager`, `Server`, power, install, backup, crash detection, events, websocket/SFTP session bags |
| `server/filesystem/` | Per-server sandboxed filesystem: path resolution, denylist, quota, archive/compress/decompress, transfer extraction |
| `server/backup/` | Local (`wings`) and S3 backup adapters |
| `server/transfer/` | Node-to-node server transfer (source side, archive streaming) |
| `server/installer/` | Create-server bootstrap (fetches config from the panel) |
| `environment/` | Environment abstraction plus Docker network setup (`docker.go`) |
| `environment/docker/` | Docker implementation: container create/attach/power/stats/cgroup burst |
| `sftp/` | Built-in SSH/SFTP server, panel-backed auth, per-op permissions |
| `internal/ufs/` | Low-level "unix filesystem" with openat/openat2-based path containment (own licenses: MIT `LICENSE`, plus BSD-3-Clause `go.LICENSE` for the Go-derived files listed in its README) |
| `internal/network/` | Internal-IP detection, allowlists, connection-limiting listener |
| `internal/database/`, `internal/models/` | SQLite (gorm) activity store, `<root>/wings.db` |
| `internal/cron/` | Activity / SFTP-event delivery to the panel |
| `parser/` | Egg config-file parsers (yaml/json/ini/xml/properties/file) |
| `events/`, `system/` | Event bus, sinks, locks, atomics, version (`system.Version`) |
| `loggers/cli/` | apex/log CLI handler |
| `protocol/` | Protocol documentation for the fork. §10 holds the Nycto additions |
| `docs/` | Feature guides for everything Nycto adds or changes, indexed in `docs/README.md` |
| `server/health.go`, `server/health_probe.go` | Health checks (TCP/HTTP/process, with log matching as the fallback), see `docs/health-checks.md` |
| `server/filesystem/disk_space.go` | Disk usage: counted only while the process can change files (`ProcessStarted`/`ProcessStopped`), adaptive interval, see `docs/disk-usage.md` |
| `internal/quota/`, `server/filesystem/project_quota.go`, `server/project_quota.go` | Optional project-quota measuring (XFS/ext4 `prjquota`), auto-detected with fallback to counting. Touches every file once with ioctls as root: security-sensitive |
| `environment/docker/events.go` | Docker events stream: stops/starts done outside Wings (`OnContainerEvent`, `StoppedExternally`), see `docs/docker-events.md` |
| `internal/metrics/`, `server/metrics.go`, `server/prometheus.go`, `router/router_metrics.go` | Metrics history (fixed-size ring file per server) and Prometheus `/metrics`, see `docs/metrics.md` |
| `server/crash.go` | Crash detection: structured `crash` event, crash-loop limit (`system.crash_detection.max_crashes`/`window`), see `docs/crash-detection.md` |
| `scripts/upstream-status.sh`, `scripts/fix-module-path.sh` | Upstream sync: see what Pterodactyl changed, and restore the module path after merging, see `docs/upstream-sync.md` |
| `scripts/install.sh` | Installer/updater for a fresh Linux server (release download or `--source` build). Keep it shellcheck-clean |
| `contrib/` | `nycto-wings.service` systemd unit. `scripts/install.sh` embeds a copy, so keep the two in sync |
| `.github/assets/` | Nycto logo/icon (`*-dark.png` = white for dark backgrounds, `*-light.png` = dark), and `originals/` with the source artwork |

### Fork names

The binary is `nycto-wings`, run by `nycto-wings.service` (`contrib/nycto-wings.service`).

Defaults, chosen so they don't clash with a stock Wings install on the same host
(`config/config_defaults_test.go` checks this):

- config: `/etc/nycto/config.yml`
- data: `/var/lib/nycto/{volumes,archives,backups}`
- logs: `/var/log/nycto/nycto-wings.log`
- temp: `/tmp/nycto`
- run dir: `/run/nycto-wings`
- system user: `nycto`
- Docker network: `nycto_nw`, bridge `nycto0`, subnets 172.19.0.0/16 and fd4e:7963:746f::/64
- logrotate file: `/etc/logrotate.d/nycto-wings`

Env overrides use the `NYCTO_*` prefix and fall back to `WINGS_*` (`config.Getenv`).

Protocol-facing names stay upstream's on purpose: the `Accept`/`User-Agent`
headers, the `wings` backup adapter and `.pteroignore`.

### Startup flow (`cmd/root.go`)

1. `initConfig`: `config.FromFile`, which runs `ResolveToken(false)` and `Set` (builds the HS256 JWT key).
2. `initLogging`.
3. Timezone, directories, system user, passwd files, logrotate.
4. `remote.New(...)`.
5. `database.Initialize`.
6. `server.NewManager`: `GET /api/remote/servers` → `InitServer` for each server.
7. `environment.ConfigureDocker`: creates the network if missing.
8. Write the config to disk.
9. Restore server states (4 workers).
10. Cron.
11. SFTP server.
12. `POST /servers/reset`.
13. HTTP/TLS listener (`router.Configure`).
14. `SIGINT`/`SIGTERM`: persist states and shut down. Containers keep running.

## Build

- **Go 1.26+** is required (`go.mod`: `go 1.26.0`, `toolchain go1.26.9`). The
  Windows dev machine has Go 1.27 at `C:\Program Files\Go\bin`. A shell that
  started before the install may need its `PATH` refreshed.
- **Wings is Linux-only.** It uses `golang.org/x/sys/unix` (openat2),
  `syscall.Getrlimit` and `//go:build unix` files, so a native Windows build
  **fails**. Always set `GOOS=linux`.

PowerShell (Windows dev machine):

```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -trimpath -ldflags="-s -w -X github.com/nycto-sh/wings/system.Version=dev" -o build/nycto-wings_linux_amd64 .
$env:GOARCH="arm64"; go build -trimpath -ldflags="-s -w" -o build/nycto-wings_linux_arm64 .
go vet ./...      # with GOOS=linux still set; type-checks Linux-only files and tests
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Bash / Linux / WSL: `make build` (amd64 + arm64 into `build/`), or

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/nycto-sh/wings/system.Version=dev" -o build/nycto-wings_linux_amd64 .
```

Lint (matches CI): `GOOS=linux golangci-lint run ./...` (config in `.golangci.yml`; `gosec` enabled).
**Run it before every push.** CI pins golangci-lint v2.11.4 on Go 1.26.9. On the Windows machine, install
that version and run it with `GOTOOLCHAIN=go1.26.9`, because the local Go 1.27 writes package data
that golangci-lint can't read:

```powershell
$env:GOBIN="<scratch dir>"; go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4
$env:GOTOOLCHAIN="go1.26.9"; $env:GOOS="linux"; golangci-lint run ./...
```

Locally, it reports every file as "not properly formatted (gofmt)" because Git checks out CRLF line
endings on Windows. Ignore those. Every other finding is real and fails CI.

**After pushing, check CI** (`https://github.com/nycto-sh/wings/actions`). It only runs on `develop`,
and the "Build and Test" job runs the tests as a **non-root** user, unlike the VPS.

Docker image: `Dockerfile` (multi-stage, distroless; `--build-arg VERSION=`).

## Tests

- Tests need **no Docker and no root**. Docker is faked with `httptest`
  (e.g. `server/install_container_test.go`). One test skips itself without root
  (`server/mounts_test.go`).
- They must **run on Linux**. On Windows, `GOOS=linux go test` builds Linux test
  binaries it can't execute. Locally, `GOOS=linux go vet ./...` is the
  compile check. Run the real tests on the VPS. WSL is available, but no
  distribution is installed (`wsl --install -d Debian` would add one).
- Commands (from CI, `.github/workflows/push.yaml`):

  ```sh
  go test ./...
  go test -race ./...
  go test ./router/...                # one package tree
  go test ./sftp -run TestName -v     # one test
  ```

- Frameworks in use: the standard `testing` package with `testify` (`require`/`assert`), plus
  some older `franela/goblin` suites. Match the file you're editing.
- Never assume Docker or root are available locally. Only build and run unit tests locally.

## Dev workflow

1. Edit locally (Windows). Run the `GOOS=linux` build and `go vet`.
2. Commit and push **directly to `develop`** on the fork (`origin` = `github.com/nycto-sh/wings`).
   Don't create feature branches or PRs, and never push to or open PRs against `pterodactyl/wings`.
3. On the Debian 12 VPS (Docker + root available), the source is at `/opt/nycto/wings-src`:
   `git pull`, `go test ./...`, build, install to `/usr/local/bin/nycto-wings`, and
   restart `nycto-wings.service` (unit in `contrib/`). Stock Pterodactyl Wings is also
   installed on that VPS (`wings.service`, `/etc/pterodactyl`), and the two must not clash.
4. Report what was verified locally versus what still needs VPS verification.

## Coding conventions observed

- `gofmt` formatting with tabs (`.editorconfig`). Keep `.go` lines to about 100 columns.
- Errors: `emperror.dev/errors` (`errors.WithStack`, `WrapIf`, `WithStackIf`).
  Some files use the stdlib `errors` package. Follow the file you're in.
- Logging: `github.com/apex/log` with structured fields
  (`log.WithField(...)`, `s.Log()`, `middleware.ExtractLogger(c)`). Never log tokens.
  `remote/http.go:debugLogRequest` redacts `Authorization`, and the router
  log line masks `token=`.
- HTTP handlers: `gin`. Read context values with the
  `middleware.Extract*` helpers. Report errors with `middleware.CaptureAndAbort(c, err)`.
  Send expected client errors as `c.AbortWithStatusJSON(code, gin.H{"error": "..."})`.
  Long-running work goes in a goroutine and returns `202 Accepted`.
- Config: read through `config.Get()`, which returns a copy. Mutate only through `config.Update(fn)` / `config.Set`.
- Concurrency: guard state with mutexes/atomics. The `system.Atomic*` types and `system.Locker` exist for this.
- Comments explain *why*. Keep the existing comment density and references (`@see` issue links).
- `nolint` directives must state a reason (`//nolint:gosec // reason`).
- Every file operation on server data goes through `server/filesystem` / `internal/ufs`,
  never raw `os.*` on user-controlled paths. (Backups use `os` because they
  live outside server-accessible directories.)

## Rules (must follow)

1. **Never weaken** path sanitization, symlink handling, archive extraction
   checks or token validation. That covers `server/filesystem/*`, `internal/ufs/*`,
   `router/tokens/*`, `router/middleware/RequireAuthorization`,
   `config.ResolveToken`, the SFTP `can()` checks, the SSRF guards in
   `router/downloader` and `router_server_backup.go`, and the websocket JWT re-validation.
2. **Flag for extra review** any change that touches file operations, SFTP, auth
   (node token, JWTs, websocket auth, denylist) or container configuration
   (`environment/docker/*`, mounts, network, users, limits). Say so clearly in
   the summary and the commit/PR description.
3. **Keep protocol compatibility.** Routes, methods, JSON field names, headers
   (including `Accept: application/vnd.pterodactyl.v1+json` and the
   `User-Agent`), websocket event names, JWT claims/scopes and `/api/remote/*`
   calls all stay as they are unless the owner explicitly approves a breaking
   change. Update `protocol/WINGS_API.md` with any protocol-visible change.
4. **Keep the upstream `LICENSE` and attribution intact.** That includes the
   "Dane Everitt & Contributors" copyright notices and `internal/ufs/LICENSE` / `go.LICENSE`.
   Add Nycto attribution alongside, never in place of, the upstream notices.
5. Don't rename upstream-specific identifiers any further without the owner's
   approval. The module path is `github.com/nycto-sh/wings`. The protocol-facing
   names listed under "Fork names" stay as they are.
6. **Document every new feature or behaviour change in the same commit.** Add or
   update a guide in `docs/` (what it does, configuration, examples, compatibility,
   troubleshooting) and list it in `docs/README.md`. Anything a panel can see or
   send also goes in `protocol/WINGS_API.md`; Nycto additions go in §10 and must
   stay optional, so the stock Pterodactyl panel keeps working.
