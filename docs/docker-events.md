# Docker events: noticing changes made outside of Wings

*Added 2026-10-13. Works on every node with Docker, with no setup.*

Nycto Wings follows Docker's event stream for server containers. When someone manages a
server's container directly with Docker, instead of through the panel, Wings notices and
reacts correctly.

## What it fixes

**`docker stop` / `docker kill` on a server's container.** Upstream Wings sees the server
stop, treats it as a crash, and **restarts it**, which undoes the admin's stop. Nycto Wings sees
that the stop came from outside, leaves the server stopped, and says so in the console:

```
The server was stopped from outside of Wings, so it will not be restarted automatically.
```

No crash is reported to the panel, and it doesn't count towards the
[crash-loop limit](crash-detection.md).

**`docker start` on a stopped server's container.** Upstream Wings doesn't notice: the server
runs with a dead console and the panel shows it as offline. Nycto Wings attaches to it, so the
console works, and marks it as running.

## How it tells the difference

Wings always marks a server as *stopping* before it stops or signals the container itself. So
when Docker reports a stop signal for a container whose server is still *running* or
*starting*, the signal came from outside of Wings.

Only signals that normally stop a program count: `SIGINT`, `SIGQUIT`, `SIGKILL` and `SIGTERM`.
Signals often used for other things, such as `docker kill -s HUP` to reload a program, are
ignored. A stop signal only explains a stop that follows within 5 minutes, and is forgotten when
the server is next started.

Likewise, Wings marks a server as *starting* before it starts the container itself. So a container
that starts while its server is *offline* was started from outside.

## Things to know

- **Docker restarting or shutting down** sends stop signals to the running containers. Their
  servers are treated as stopped from outside, so Wings doesn't fight Docker by restarting them.
- If the event stream is lost, for example while Docker restarts, Wings reconnects on its own,
  waiting a little longer after each failed attempt, up to 30 seconds. Changes made while it was
  disconnected are handled the old way.
- Only containers of servers on this node are affected. Installer containers and containers of
  another Wings install on the same machine are ignored.

## Compatibility

There's nothing to configure, and nothing changes for the panel. The panel sees the usual
`status` changes: `offline` after an outside stop, and `running` after an outside start.
