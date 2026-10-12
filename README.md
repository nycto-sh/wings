<p align="center">
  <a href="https://github.com/nycto-sh/wings">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset=".github/assets/logo-dark.png">
      <img alt="Nycto" src=".github/assets/logo-light.png" height="72">
    </picture>
  </a>
</p>

# Nycto Wings

Nycto Wings is the node daemon for **Nycto**, an open-source game and app server hosting panel. It runs on
each machine that hosts servers. It starts every server in its own Docker container, streams the console
over a websocket, manages files and backups, and runs a built-in SFTP server. A panel controls it through
an HTTP API.

Nycto Wings is a fork of [Pterodactyl Wings](https://github.com/pterodactyl/wings) and speaks the same
protocol, so it works with both the **Nycto panel** and the **stock Pterodactyl panel**. Any departure from
that protocol will be deliberate and announced. The full panel ⇄ daemon contract is documented in
[`protocol/WINGS_API.md`](protocol/WINGS_API.md).

> **Status:** early development. Nycto Wings follows upstream Wings closely and is tested against the
> Pterodactyl panel.

## Requirements

- Linux on `amd64` or `arm64`, with systemd
- Docker (the installer can set it up for you)
- A Nycto or Pterodactyl panel with a node created for this machine

## Install

Run the installer as root on the machine that will host servers:

```bash
curl -fsSL https://raw.githubusercontent.com/nycto-sh/wings/develop/scripts/install.sh | sudo bash
```

It installs Docker if it's missing, puts the `nycto-wings` binary in `/usr/local/bin`, creates `/etc/nycto`,
and installs and enables `nycto-wings.service`.

To connect the node in the same step, pass the details from the panel. In the panel, open the node's
**Configuration** tab and click **Generate Token**. The installer asks for the token, so it never ends up in
your shell history:

```bash
curl -fsSL https://raw.githubusercontent.com/nycto-sh/wings/develop/scripts/install.sh \
  | sudo bash -s -- --panel-url https://panel.example.com --node 3
```

| Option | Meaning |
|---|---|
| `--panel-url URL`, `--node ID` | Configure the node against this panel, then start the service |
| `--token TOKEN` | Panel API token. If omitted, you're asked for it |
| `--source` | Build from source instead of downloading a release. Use this until the first release is published |
| `--ref REF` | Git branch or tag to build with `--source` (default `develop`) |
| `--version TAG` | Release to download (default: latest) |
| `--skip-docker` | Don't install Docker |
| `-y`, `--yes` | Don't ask for confirmation |

Running the installer again updates Nycto Wings in place and restarts the service. Your configuration is kept.

### Manual install

```bash
# Build (Go 1.26 or newer)
git clone https://github.com/nycto-sh/wings.git && cd wings
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/nycto-sh/wings/system.Version=dev" -o nycto-wings .
sudo install -m 755 nycto-wings /usr/local/bin/nycto-wings

# Configure and start
sudo mkdir -p /etc/nycto
sudo nycto-wings configure --panel-url https://panel.example.com --token <token> --node <id>
sudo cp contrib/nycto-wings.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now nycto-wings
```

## Running beside Pterodactyl Wings

Nycto Wings uses its own names and paths, so it can share a machine with an existing Pterodactyl Wings
install. In the panel, give the Nycto node a different daemon port and SFTP port, for example `8081` and
`2023`.

|  | Pterodactyl Wings | Nycto Wings |
|---|---|---|
| Binary / service | `wings` / `wings.service` | `nycto-wings` / `nycto-wings.service` |
| Configuration | `/etc/pterodactyl/config.yml` | `/etc/nycto/config.yml` |
| Server data | `/var/lib/pterodactyl` | `/var/lib/nycto` |
| Logs | `/var/log/pterodactyl/wings.log` | `/var/log/nycto/nycto-wings.log` |
| System user | `pterodactyl` | `nycto` |
| Docker network / bridge | `pterodactyl_nw` / `pterodactyl0` (172.18.0.0/16) | `nycto_nw` / `nycto0` (172.19.0.0/16) |
| Environment overrides | `WINGS_*` | `NYCTO_*`, falling back to `WINGS_*` |

## Development

Nycto Wings only builds for Linux. On other systems, set `GOOS=linux`:

```bash
GOOS=linux go build ./...      # build
GOOS=linux go vet ./...        # vet, including the Linux-only files
go test ./...                  # tests, on Linux; they need neither Docker nor root
make build                     # release binaries for amd64 and arm64 in build/
```

Changes that touch file handling, SFTP, authentication or container configuration get extra review. Keep
protocol compatibility unless a break has been agreed. Contributor notes are in [`CLAUDE.md`](CLAUDE.md).

## Features

Nycto Wings adds features on top of Pterodactyl Wings. They're all optional, so the stock
Pterodactyl panel keeps working. Guides are in [`docs/`](docs/README.md):

- [Health checks](docs/health-checks.md): servers are marked online when they actually accept
  connections (TCP, HTTP or process checks) rather than when the console prints a line,
  with live healthy/unhealthy states.
- [Crash explanations and crash-loop protection](docs/crash-detection.md): the panel is told why a
  server crashed ("ran out of memory (2 GB limit)"), with its last console lines, and a server that
  keeps crashing is no longer restarted forever.

## Reporting issues

Report bugs and request features in [this repository's issues](https://github.com/nycto-sh/wings/issues).
For security problems, please don't open a public issue. Use
[GitHub's private vulnerability reporting](https://github.com/nycto-sh/wings/security/advisories/new) instead.

## License and attribution

Nycto Wings is released under the [MIT license](LICENSE). It is based on
[Pterodactyl Wings](https://github.com/pterodactyl/wings), Copyright © 2018 - 2021 Dane Everitt and
contributors, and keeps its license and copyright notices. `internal/ufs` is MIT licensed
(Copyright © 2024 Matthew Penner), and some of its files are derived from the Go project under the
BSD-3-Clause license. See [`internal/ufs/README.md`](internal/ufs/README.md).
