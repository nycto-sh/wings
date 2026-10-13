# Bringing in upstream changes

*Added 2026-10-13.*

Nycto Wings is a fork of [Pterodactyl Wings](https://github.com/pterodactyl/wings). Pterodactyl
keeps fixing bugs and security issues in Wings, and the fork should get those fixes. This is how.

## Where the fork stands

The fork was made from upstream `develop` at `5d9e2c3` (2026-10-09). That's release **v1.13.3**
plus 23 later upstream commits. Everything since is Nycto's own work on top. Because the fork
shares upstream's history, upstream changes can be **merged** in, the same way as any other branch.

Upstream is only ever read from. Its remote is set up so that pushing to it is impossible:

```
upstream  https://github.com/pterodactyl/wings.git (fetch)
upstream  DISABLED (push)
```

## When to check

- Whenever Pterodactyl publishes a Wings release or a security advisory. On GitHub, open
  [pterodactyl/wings](https://github.com/pterodactyl/wings), click **Watch → Custom**, and tick
  **Releases** and **Security alerts** to get notified.
- Otherwise, every few weeks.

## 1. See what changed

```bash
scripts/upstream-status.sh
```

It adds the `upstream` remote the first time, fetches it, and shows:

- the latest upstream commit and release, and what the fork is based on
- every upstream commit that isn't in the fork yet, and the files they change
- which of those files are **security-sensitive** (file handling, SFTP, tokens and auth, the
  websocket, Docker, backups, transfers, remote downloads), so they get a careful review

If it says *"The fork has every upstream change"*, you're done.

## 2. Merge them in

On an up-to-date `develop`, with no uncommitted changes:

```bash
git checkout develop && git pull
git merge upstream/develop
```

**If there are conflicts**, they'll be where Nycto deliberately differs from upstream. Keep the
Nycto side of these, and take upstream's changes everywhere else:

| Where | Keep from Nycto |
|---|---|
| `config/config.go`, `config/config_docker.go` | The Nycto defaults: `/etc/nycto`, `/var/lib/nycto`, user `nycto`, network `nycto_nw` / `nycto0`, `NYCTO_*` variables, and the Nycto config sections (`health_checks`, `disk_quota`, `metrics`, crash limits) |
| `cmd/root.go` | The `nycto-wings` name and banner, which keeps the upstream copyright notice, plus the start-up of the events watcher, project quotas and metrics |
| `README.md`, `Makefile`, `Dockerfile`, `.github/`, `contrib/`, `scripts/` | Nycto versions |
| `server/` and `environment/` files with Nycto features | Both sides: upstream's change **and** the Nycto hooks, such as the health, crash, disk usage and metrics calls. See `CLAUDE.md` for which files hold them |
| `docs/`, `protocol/`, `CLAUDE.md` | Nycto versions |

After resolving each file: `git add <file>`. Then:

## 3. Fix the module path

Upstream code imports `github.com/pterodactyl/wings/...`, and the fork's module is
`github.com/nycto-sh/wings`. Change any upstream imports back:

```bash
scripts/fix-module-path.sh
```

It only touches Go code, `go.mod`, the build files and CI, never links to the upstream site, and
puts the imports back in order with `gofmt`.

## 4. Check it

```bash
GOOS=linux go build ./... && GOOS=linux go vet ./...
GOOS=linux golangci-lint run ./...     # see CLAUDE.md for the exact version CI uses
```

Then commit the merge, push to `develop`, and:

- **Check CI** on GitHub (Actions). It runs the tests as an ordinary user.
- **On the VPS:** `git pull`, `go test ./...`, build, install, restart, and check a server still
  starts, stops and shows its console.
- **If upstream changed anything the panel sees** (routes, events, tokens, the remote API),
  update `protocol/WINGS_API.md`. Keep compatibility with the stock panel.

## Just one urgent fix

When only one upstream commit is needed right away, for example a security fix, take just that one:

```bash
git cherry-pick -x <upstream commit>
scripts/fix-module-path.sh && git commit -am "Use the fork's module path in <what was picked>"
```

`-x` records which upstream commit it came from. The next full merge sees that the change is
already there.

## Things never to do

- Push to `upstream`. It's disabled, and it should stay that way.
- Rewrite the fork's history to match upstream (`git reset --hard upstream/develop`, rebasing
  `develop` onto upstream). That would throw away every Nycto change.
- Remove the upstream copyright or `LICENSE` while resolving conflicts.
