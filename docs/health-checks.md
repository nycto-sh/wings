# Health checks

*Added 2026-10-13. Protocol spec: [`protocol/WINGS_API.md` §10.1](../protocol/WINGS_API.md#101-health-checks).*

Health checks let Nycto Wings decide for itself when a server is ready for players,
and notice when a running server stops responding. They replace the old approach of
waiting for a line such as `Done (` in the console, which keeps working as a fallback.

## Why

Without a health check, a server is shown as online as soon as its console prints the
egg's "done" line. That goes wrong in three common ways:

- **Stuck on "starting".** A modpack or custom jar prints a different message, so the
  line never appears, even though players can join.
- **Online too early.** The line is printed, but the port isn't open yet, or the server
  crashes a moment later while still loading.
- **Online while frozen.** A deadlocked server keeps its container running, so it
  looks online while nobody can connect.

A health check tests the server the way a player or client would, and keeps testing it
while it runs.

## Check types

| Type | Healthy when | Use it for |
|---|---|---|
| `tcp` | A connection to the server's port succeeds | Most game servers that use TCP, such as Minecraft Java Edition |
| `http` | A web request to the server returns a success status | Web apps, APIs, bots with a status page |
| `process` | The server's program is running | Bots and programs with no port and no reliable "done" line |
| `log` | The console prints the egg's "done" line | Anything else. This is the default, and the only behaviour the stock Pterodactyl panel uses |

**UDP games** (Minecraft Bedrock, Valheim, Rust, CS2 and others) can't be tested with
`tcp`. Use `process`, or leave them on `log`.

**`process` doesn't notice a frozen program.** A program that hangs is still running as far
as the system is concerned, so a `process` check stays healthy. Only `tcp` and `http`
checks catch a server that is running but no longer responding.

## What customers see

A server's health is one of:

| Health | Meaning |
|---|---|
| **Offline** | The server isn't running. |
| **Starting** | It's booting and hasn't passed its check yet. |
| **Healthy** | The last check passed, so players can connect. |
| **Unhealthy** | It stopped passing its check, or never passed it after starting. |

Each health comes with a short message the panel can show as-is, for example
*"port 25565 is accepting connections"* or *"port 25565 did not answer within 5 seconds"*.
Messages never include internal addresses.

Changes are pushed to the panel over the console websocket the moment they happen, so
the panel can update instantly. While a server is starting it's checked every 2
seconds, so it turns healthy within about 2 seconds of being ready.

The existing server status (offline / starting / running / stopping) still works the way
it always has. With a health check, "running" simply means the check has passed, instead
of meaning that the console printed a line.

## Configuring a check

The panel sends the check with the server's settings, as a `health_check` object. A server
without one uses `log`. For the Nycto panel, this would typically be set per egg, with a
per-server override.

Minecraft Java Edition, checking the server's main port:

```json
{ "type": "tcp" }
```

A web app with a health endpoint on a second allocated port, restarted if it hangs:

```json
{
  "type": "http",
  "port": 8080,
  "path": "/health",
  "expected_status": 200,
  "restart_on_unhealthy": true
}
```

A Discord bot:

```json
{ "type": "process" }
```

All settings:

| Setting | Default | Range | Meaning |
|---|---|---|---|
| `type` | `log` | `log`, `tcp`, `http`, `process` | How the server is checked |
| `port` | default allocation | the server's allocated ports | Port for `tcp` and `http` checks |
| `path` | `/` | a path, with an optional query | Path requested by `http` checks |
| `expected_status` | any 200–399 | 100–599 | Exact status code an `http` check expects |
| `interval` | 10 | 2–300 seconds | Time between checks once healthy |
| `timeout` | 5 | 1–60 seconds, at most `interval` | Time one check may take |
| `start_period` | 300 | 1–3600 seconds | Time the server has to become healthy after starting |
| `retries` | 3 | 1–20 | Failed checks in a row before a healthy server is unhealthy |
| `restart_on_unhealthy` | `false` | | Restart the server once when it becomes unhealthy |

Values that are missing or `0` use the default, and values outside the range are
brought within it.

Wings reads the settings again whenever the panel syncs the server
(`POST /api/servers/:server/sync`, and on every start), so the panel should sync after
changing them. A running server picks up the new settings without a restart, including
when it switches from `log` to a check that probes it.

## Setting checks on the node

Panels that don't send health checks, such as the stock Pterodactyl panel, can still use
them. The node operator assigns checks in `/etc/nycto/config.yml`, either for every server
of an egg or for a single server:

```yaml
health_checks:
  eggs:
    # Every server of this egg, for example Minecraft Java Edition.
    5d1b8a7c-0000-4000-8000-000000000000:
      type: tcp
  servers:
    # One server, which takes priority over its egg's check.
    9568b9c8-5b9e-42e6-ab2a-f4ed9ed7ad90:
      type: http
      port: 8080
      path: /health
      restart_on_unhealthy: true
```

The settings are the same as in the table above, written in YAML.

**Which check applies.** The first of these that exists:

1. a check sent by the panel
2. the server's entry under `servers`
3. the server's egg's entry under `eggs`
4. otherwise, `log`

To force the old behaviour for one server of an egg that has a check, give that server
`type: log`.

**Applying changes.** Wings reads this section when it starts, so run
`systemctl restart nycto-wings` after editing it. Game servers keep running through a
Wings restart, and their checks start right away.

**Finding the UUIDs.** Wings lists every server on the node with its UUID and egg UUID.
Run this on the node, with your node's address and daemon port (install `jq` with
`apt install jq` if needed):

```bash
TOKEN=$(awk '$1 == "token:" {print $2}' /etc/nycto/config.yml)
curl -s -H "Authorization: Bearer $TOKEN" https://node.example.com:8081/api/servers \
  | jq -r '.[] | "\(.configuration.uuid)  egg=\(.configuration.egg.id)  \(.configuration.meta.name)  health=\(.health.status)"'
```

**Watching it work.** Every change of health is logged:

```bash
journalctl -u nycto-wings -f | grep "server health changed"
```

## How it behaves

1. **Start.** Every start or restart begins in **Starting**.
2. **First pass.** The first successful check makes the server **Healthy**, and its status
   becomes "running".
3. **Never ready.** If the server doesn't pass within `start_period`, it becomes
   **Unhealthy** but stays "starting". It's never restarted for this, so a broken server
   can't get stuck in a restart loop. If it passes later, it becomes Healthy.
4. **Stops responding.** A healthy server that fails `retries` checks in a row becomes
   **Unhealthy**. One successful check makes it Healthy again.
5. **Restart.** With `restart_on_unhealthy`, a server that goes from Healthy to Unhealthy
   is restarted once, and the console shows
   *"Server failed N health checks in a row, restarting..."*.
6. **Stop.** Stopping the server makes it **Offline**.

## Security

`tcp` and `http` checks only connect to the server's own container, on one of the ports
allocated to that server. A check can't be pointed at another server, the node itself, or
any other address, and an `http` path can only change the path and query, never the host.
Redirects aren't followed.

## Troubleshooting

**The server stays Starting, then turns Unhealthy, but players can join.**
- Make sure the check uses the port players connect to, and that this port is allocated
  to the server. The message says `port N is not allocated to this server` if it isn't.
- The server must listen on all interfaces inside its container (`0.0.0.0`), not only on
  `127.0.0.1`. Players couldn't reach it otherwise either.
- UDP-only games can't pass a `tcp` check. Use `process` or `log`.

**An HTTP check fails with a 3xx status.**
Redirects aren't followed. Point `path` at the final URL, or set `expected_status` to the
redirect code.

**I want the old behaviour.**
Remove the `health_check` setting, or set `"type": "log"`.

## Compatibility

Everything is optional. The stock Pterodactyl panel never sends `health_check`, so its
servers keep the "done" line behaviour unless the node assigns them a check
([Setting checks on the node](#setting-checks-on-the-node)). The new `health` websocket
event and `health` field in the server details are ignored by panels that don't use them.
