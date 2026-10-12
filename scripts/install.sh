#!/usr/bin/env bash
#
# Installs or updates Nycto Wings on a Linux server.
#
#   curl -fsSL https://raw.githubusercontent.com/nycto-sh/wings/develop/scripts/install.sh | sudo bash
#
# Run it with --help for the options. Running it again updates Nycto Wings in
# place and keeps the existing configuration.

set -euo pipefail

REPO="nycto-sh/wings"
MODULE="github.com/nycto-sh/wings"
BIN="/usr/local/bin/nycto-wings"
CONFIG_DIR="/etc/nycto"
CONFIG="$CONFIG_DIR/config.yml"
UNIT="/etc/systemd/system/nycto-wings.service"
SRC_DIR="/opt/nycto/wings-src"
GO_ROOT="/opt/nycto/go"

source_build=false
ref="develop"
version="latest"
skip_docker=false
assume_yes=false
panel_url=""
node=""
token=""

usage() {
	cat <<'EOF'
Installs or updates Nycto Wings.

Usage: install.sh [options]

  --panel-url URL   Configure the node against this panel (needs --node)
  --node ID         The node's ID in the panel
  --token TOKEN     Panel API token; asked for if --panel-url is given without it
  --source          Build from source instead of downloading a release
  --ref REF         Branch or tag to build with --source (default: develop)
  --version TAG     Release to download (default: latest)
  --skip-docker     Do not install Docker if it is missing
  -y, --yes         Do not ask for confirmation
  -h, --help        Show this help
EOF
}

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
	exit 1
}

# Prompts read from the terminal, since stdin is this script when it is piped
# into bash.
confirm() {
	if $assume_yes; then
		return 0
	fi
	[ -r /dev/tty ] || die "cannot ask for confirmation without a terminal; pass --yes"
	local reply
	read -r -p "$1 [y/N] " reply </dev/tty
	[[ $reply =~ ^[Yy]$ ]]
}

while [ $# -gt 0 ]; do
	case "$1" in
	--panel-url) panel_url="${2:?--panel-url needs a value}"; shift 2 ;;
	--node) node="${2:?--node needs a value}"; shift 2 ;;
	--token) token="${2:?--token needs a value}"; shift 2 ;;
	--source) source_build=true; shift ;;
	--ref) ref="${2:?--ref needs a value}"; shift 2 ;;
	--version) version="${2:?--version needs a value}"; shift 2 ;;
	--skip-docker) skip_docker=true; shift ;;
	-y | --yes) assume_yes=true; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

# --- Checks -------------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "run this script as root (for example with sudo)"
[ "$(uname -s)" = "Linux" ] || die "Nycto Wings only runs on Linux"
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || die "systemd is required"
command -v curl >/dev/null 2>&1 || die "curl is required"

case "$(uname -m)" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
*) die "unsupported CPU architecture: $(uname -m)" ;;
esac

if [ -n "$panel_url" ] || [ -n "$node" ]; then
	[ -n "$panel_url" ] && [ -n "$node" ] || die "--panel-url and --node must be given together"
	[[ $node =~ ^[0-9]+$ ]] || die "--node must be the node's numeric ID"
	if [ -z "$token" ]; then
		[ -r /dev/tty ] || die "pass --token, or run the installer from a terminal to be asked for it"
		read -r -s -p "Panel API token: " token </dev/tty
		echo
		[ -n "$token" ] || die "no token given"
	fi
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

install_packages() {
	if command -v apt-get >/dev/null 2>&1; then
		DEBIAN_FRONTEND=noninteractive apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y -q "$@"
	elif command -v yum >/dev/null 2>&1; then
		yum install -y -q "$@"
	else
		die "install these packages and run the installer again: $*"
	fi
}

# --- Docker -------------------------------------------------------------------

if command -v docker >/dev/null 2>&1; then
	log "Docker is installed"
elif $skip_docker; then
	warn "Docker is not installed. Nycto Wings will not run until it is."
else
	confirm "Docker is not installed. Install it with Docker's script from get.docker.com?" \
		|| die "Docker is required; install it, or pass --skip-docker"
	log "Installing Docker"
	curl -fsSL https://get.docker.com -o "$tmp/get-docker.sh"
	CHANNEL=stable sh "$tmp/get-docker.sh"
fi
if command -v docker >/dev/null 2>&1; then
	systemctl enable --now docker >/dev/null
fi

# --- Binary -------------------------------------------------------------------

download_release() {
	local base asset="nycto-wings_linux_$arch"
	if [ "$version" = "latest" ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$version"
	fi

	log "Downloading $asset ($version)"
	curl -fsSL -o "$tmp/$asset" "$base/$asset" \
		|| die "could not download $asset from $base. If no release has been published yet, run the installer with --source."

	if curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"; then
		(cd "$tmp" && grep " $asset\$" checksums.txt | sha256sum -c --quiet -) \
			|| die "the checksum of $asset does not match the release"
	else
		warn "the release has no checksums.txt, so the download could not be verified"
	fi
	mv "$tmp/$asset" "$tmp/nycto-wings"
}

# go_version prints the Go toolchain the source requires, for example go1.26.9.
go_version() {
	local v
	v="$(awk '$1 == "toolchain" { print $2 }' "$SRC_DIR/go.mod")"
	if [ -z "$v" ]; then
		v="go$(awk '$1 == "go" { print $2 }' "$SRC_DIR/go.mod")"
	fi
	echo "$v"
}

# install_go installs the Go toolchain used for building into $GO_ROOT, so that
# any Go already on the system is left alone.
install_go() {
	local want have="" tarball
	want="$(go_version)"
	if [ -x "$GO_ROOT/bin/go" ]; then
		have="$(GOTOOLCHAIN=local "$GO_ROOT/bin/go" env GOVERSION)"
	fi
	if [ "$have" = "$want" ]; then
		return
	fi

	tarball="$want.linux-$arch.tar.gz"
	log "Installing Go $want into $GO_ROOT"
	curl -fsSL -o "$tmp/$tarball" "https://dl.google.com/go/$tarball" || die "could not download $tarball"
	if curl -fsSL -o "$tmp/$tarball.sha256" "https://dl.google.com/go/$tarball.sha256"; then
		echo "$(cat "$tmp/$tarball.sha256")  $tmp/$tarball" | sha256sum -c --quiet - \
			|| die "the checksum of $tarball does not match"
	else
		warn "could not download the checksum for $tarball, so it could not be verified"
	fi
	rm -rf "$GO_ROOT"
	mkdir -p "$(dirname "$GO_ROOT")"
	tar -C "$(dirname "$GO_ROOT")" -xzf "$tmp/$tarball"
}

build_from_source() {
	command -v git >/dev/null 2>&1 || install_packages git

	if [ -d "$SRC_DIR/.git" ]; then
		[ -z "$(git -C "$SRC_DIR" status --porcelain)" ] \
			|| die "$SRC_DIR has local changes; commit or discard them first"
		log "Updating the source in $SRC_DIR to $ref"
		git -C "$SRC_DIR" fetch --quiet --prune --tags origin
		if git -C "$SRC_DIR" ls-remote --exit-code --heads origin "$ref" >/dev/null 2>&1; then
			git -C "$SRC_DIR" checkout --quiet "$ref"
			git -C "$SRC_DIR" merge --quiet --ff-only "origin/$ref"
		else
			git -C "$SRC_DIR" checkout --quiet --detach "refs/tags/$ref" || die "no branch or tag called $ref"
		fi
	else
		log "Downloading the source ($ref) into $SRC_DIR"
		mkdir -p "$(dirname "$SRC_DIR")"
		git clone --quiet --branch "$ref" "https://github.com/$REPO.git" "$SRC_DIR"
	fi

	install_go

	local v
	v="dev-$(git -C "$SRC_DIR" rev-parse --short HEAD)"
	if tag="$(git -C "$SRC_DIR" describe --tags --exact-match 2>/dev/null)"; then
		v="${tag#v}"
	fi

	log "Building Nycto Wings $v"
	(
		cd "$SRC_DIR"
		PATH="$GO_ROOT/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0 \
			go build -trimpath -ldflags "-s -w -X $MODULE/system.Version=$v" -o "$tmp/nycto-wings" .
	)
}

if $source_build; then
	build_from_source
else
	download_release
fi

# Check that the new binary runs before replacing the old one. It is run from
# next to its destination, since /tmp may be mounted noexec. Renaming it into
# place works while the old binary is running.
install -m 755 "$tmp/nycto-wings" "$BIN.new"
if ! "$BIN.new" version >/dev/null; then
	rm -f "$BIN.new"
	die "the new binary does not run on this machine"
fi
mv -f "$BIN.new" "$BIN"
log "Installed $("$BIN" version | head -n 1) to $BIN"

# --- Service ------------------------------------------------------------------

# Keep this in sync with contrib/nycto-wings.service.
cat >"$tmp/nycto-wings.service" <<'EOF'
[Unit]
Description=Nycto Wings Daemon
After=docker.service
Requires=docker.service
PartOf=docker.service

[Service]
User=root
WorkingDirectory=/etc/nycto
LimitNOFILE=4096
ExecStart=/usr/local/bin/nycto-wings --config /etc/nycto/config.yml
Restart=on-failure
StartLimitInterval=180
StartLimitBurst=30
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
if ! cmp -s "$tmp/nycto-wings.service" "$UNIT"; then
	install -m 644 "$tmp/nycto-wings.service" "$UNIT"
	systemctl daemon-reload
	log "Installed $UNIT"
fi
systemctl enable nycto-wings >/dev/null 2>&1

# --- Configuration ------------------------------------------------------------

install -d -m 700 "$CONFIG_DIR"

if [ -n "$panel_url" ]; then
	if [ ! -f "$CONFIG" ] || confirm "$CONFIG already exists. Replace the panel settings in it?"; then
		log "Configuring node $node against $panel_url"
		"$BIN" configure --panel-url "$panel_url" --token "$token" --node "$node" \
			--config-path "$CONFIG" --override
	fi
fi

if systemctl cat wings.service >/dev/null 2>&1; then
	warn "Pterodactyl Wings is also installed here. Give the Nycto node different daemon and SFTP ports in the panel (for example 8081 and 2023)."
fi

if [ -f "$CONFIG" ]; then
	systemctl restart nycto-wings
	log "Nycto Wings is running. Follow the log with: journalctl -u nycto-wings -f"
else
	cat <<EOF

Nycto Wings is installed but not configured yet. In the panel, open the node's
Configuration tab, click "Generate Token", and run:

  nycto-wings configure --panel-url <panel url> --token <token> --node <node id>
  systemctl start nycto-wings

EOF
fi
