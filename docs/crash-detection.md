# Crash explanations and crash-loop protection

*Added 2026-10-13. Protocol spec: [`protocol/WINGS_API.md` §10.2](../protocol/WINGS_API.md#102-crash-explanations).*

When a server crashes, Nycto Wings tells the panel **why** in a form it can show to customers,
such as *"The server ran out of memory (2 GB limit) and was stopped."*. It also stops restarting
a server that keeps crashing, instead of restarting it forever.

## What counts as a crash

A crash is when a server's process stops without anyone pressing Stop: it exits with an
error, runs out of memory, or is killed. By default a server that exits cleanly (exit code 0)
also counts, because a game server that quits on its own usually shouldn't have. Set
`detect_clean_exit_as_crash: false` on the node to change that.

## What the panel and customers see

Every crash produces a `crash` event with:

| Field | Example | Meaning |
|---|---|---|
| `reason` | `out_of_memory` | Why it crashed, see below |
| `message` | *The server ran out of memory (2 GB limit) and was stopped.* | A sentence the panel can show as-is |
| `exit_code` | `137` | The process's exit code |
| `oom_killed` | `true` | Whether it was stopped for running out of memory |
| `memory_limit_bytes` | `2147483648` | The server's memory limit, `0` for unlimited |
| `uptime_seconds` | `412` | How long it ran before crashing |
| `crashed_at` | `2026-10-13T20:14:03Z` | When it crashed |
| `last_logs` | `["...", "java.lang.OutOfMemoryError: Java heap space"]` | The last lines of console output, without colours |
| `restarting` | `false` | Whether Wings is restarting it |
| `reason_not_restarting` | `crash_loop` | Why not, when `restarting` is false |
| `crashes_in_window` | `3` | Crashes within the crash-loop window, including this one |

The reasons:

| `reason` | When | Message |
|---|---|---|
| `out_of_memory` | It used more memory than its limit | *The server ran out of memory (2 GB limit) and was stopped.* |
| `killed` | Exit code 137 without the out-of-memory flag | *The server was killed (exit code 137). This usually means it ran out of memory or was force-stopped.* |
| `terminated` | Exit code 143 | *The server was stopped by a termination signal (exit code 143).* |
| `error` | Any other non-zero exit code | *The server crashed with exit code 1.* Common signals are named, e.g. *(segmentation fault)* |
| `clean_exit` | Exit code 0 | *The server stopped on its own without reporting an error.* |

The console also shows the explanation, after the lines it has always shown:

```
---------- Detected server process in a crashed state! ----------
Exit code: 137
Out of memory: true
The server ran out of memory (2 GB limit) and was stopped.
```

The panel can also read the most recent crash from the server details (`last_crash`). If a
server is offline because of a crash it wasn't restarted after, anyone who opens its console
later gets the `crash` event again, so the explanation isn't lost.

`last_logs` only contains console output that anyone who can open the console already sees.

## Crash-loop protection

After a crash, Wings restarts the server unless one of these applies:

| Rule | Default | `reason_not_restarting` |
|---|---|---|
| Crash detection is off for the server, or for the node | on | `crash_detection_disabled` |
| The previous crash after which it was restarted was less than `timeout` seconds ago | 60 seconds | `too_frequent` |
| It has crashed `max_crashes` times within `window` seconds | 3 times in 10 minutes | `crash_loop` |

When it stops restarting because of a crash loop, the console says:

```
Aborting automatic restart, the server crashed 3 times in 10 minutes. Start it again once the problem is fixed.
```

**Starting the server again clears its crash history.** That covers a start from the panel, an
API call, a schedule, or a Wings restart. The customer can always try again after fixing the
problem, and get the full number of attempts. Restarts that Wings makes by itself, after a crash
or because a [health check](health-checks.md) failed, don't clear the history.

Upstream Wings only had the 60-second rule, so a server that crashed every 90 seconds was
restarted forever. The `max_crashes` rule closes that gap.

## Configuration

In `/etc/nycto/config.yml`, under `system.crash_detection`:

```yaml
system:
  crash_detection:
    enabled: true                     # restart crashed servers at all
    detect_clean_exit_as_crash: true  # count exit code 0 as a crash
    timeout: 60                       # don't restart if the last crash was less than this many seconds ago; 0 = off
    max_crashes: 3                    # stop restarting after this many crashes within the window; 0 = no limit
    window: 600                       # seconds over which crashes are counted
    log_lines: 25                     # console lines included in the crash event (up to 100)
```

Run `systemctl restart nycto-wings` after changing it. Game servers keep running.

To get the upstream behaviour back, set `max_crashes: 0`.

Each line in `last_logs` is cut at 1000 bytes.

## Compatibility

The stock Pterodactyl panel ignores the `crash` event and the `last_crash` field. It still shows
the console lines as before, now with the explanation line added. The crash-loop limit applies to
every server, including on a stock panel. That's a deliberate change from upstream Wings, and
`max_crashes: 0` turns it off.
