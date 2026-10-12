# Disk usage checking

*Changed 2026-10-13. Works on every Linux VPS and filesystem, with no setup.*

Wings has to know how much disk space each server uses, both to show it in the panel and to
stop a server that goes over its limit. The only way that works on every filesystem is to
count the size of every file in the server's folder. Nycto Wings still does that, but much
less often than upstream Wings.

## What changed

**Starting a server no longer waits for a count.** Upstream Wings counts every file before
each start, which is the *"Checking server disk space usage, this could take a few
seconds..."* line in the console. For a modpack with hundreds of thousands of files that's
seconds of heavy disk reading before the server even begins to boot. Nycto Wings skips it
whenever the count it already has is known to be exact (see below), which is almost always.
The line only appears when a count is really needed.

**Offline servers are never counted again.** While a server is offline, its files can only
change through Wings (the file manager, SFTP, uploads, decompressing, backup restores), and
Wings updates the count as those happen. So once a server has stopped and been counted once,
its count stays exact until it runs again, however long it stays offline.

**Running servers are counted less often when they have plenty of room.** Upstream Wings
counts every running server every `disk_check_interval` seconds (150 by default). Nycto Wings
uses that interval for servers close to their limit, and stretches it for servers far from it:

| Server's usage | Counted every | With the default 150 s |
|---|---|---|
| No disk limit, or under 25% of it | 4 × `disk_check_interval` | 10 minutes |
| Under 50% of its limit | 2 × `disk_check_interval` | 5 minutes |
| 50% of its limit or more | `disk_check_interval` | 2½ minutes |

**Counting is faster when there are many hard links.** Files with several names (hard links)
are counted once. Checking whether one had already been counted used to get slower with every
hard link found; now it takes the same time however many there are.

## How the count stays exact

| Event | What Wings does |
|---|---|
| The server starts | Its files can now change without Wings knowing, so it's counted on the timer above |
| The server stops | It's counted once more, in the background, right away |
| The installer runs | Same as the server running: counted once more when the installer finishes |
| Changes through Wings while offline | Added to or taken from the count as they happen, with no counting needed |
| Wings starts | Every server is counted once |

## Configuration

In `/etc/nycto/config.yml`, under `system`:

```yaml
system:
  disk_check_interval: 150     # seconds; 0 turns disk usage checking off completely
  adaptive_disk_check: true    # count servers far from their limit less often
```

Set `adaptive_disk_check: false` to count every running server every `disk_check_interval`
seconds, as upstream Wings does. Run `systemctl restart nycto-wings` after changing either
setting. Game servers keep running.

## Things to know

- **Files changed directly on the node while a server is offline**, for example by an admin
  over SSH, aren't noticed until the server next runs, or until Wings restarts. Changes through
  the panel, SFTP or the API are always counted.
- **A running server far from its limit is checked less often**, so a sudden burst of writes is
  noticed a little later than before: up to 10 minutes instead of 2½ for a server under 25% of
  its limit. Writes through Wings (file manager, SFTP, uploads) are still checked against the
  limit immediately. Turn `adaptive_disk_check` off if you need the faster checks.

## Compatibility

Nothing changes for the panel. Disk usage is reported in the same place, in the same way. The
only visible difference is the missing *"Checking server disk space usage..."* line on most starts.
