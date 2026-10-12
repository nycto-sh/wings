# Disk usage checking

*Changed 2026-10-13. Works on every Linux VPS and filesystem, with no setup. Uses project quotas
automatically where the disk supports them.*

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

## Project quotas (optional speed-up)

Some filesystems can keep a running total of the space used by each folder, called **project
quotas**. When the disk that holds the server data has them turned on, Nycto Wings uses them
automatically. It reads a server's usage from the kernel in one quick call, and never counts its
files again. When they aren't available, nothing changes and Wings counts files as described above.

| Disk | Project quotas |
|---|---|
| XFS mounted with the `prjquota` option | ✅ |
| ext4 with the `project` and `quota` features, mounted with `prjquota` | ✅ |
| ext4 as set up by most VPS providers, Btrfs, ZFS, overlay and others | ❌ counts files as usual |

It also needs Linux 5.14 or newer, and Wings running as root directly on the machine. In rootless
mode, or with Wings inside a container, it counts files as usual.

### What happens when they're available

When Wings starts, it gives every server folder its own *project ID*. The ID is stored on the folder
itself, so it survives restarts and needs no database. Everything created in the folder later gets
the same ID automatically. Giving the existing files their ID visits each of them once, one server
at a time, in the background. Until a server is done, its files are counted as usual.

The Wings log shows which method is in use:

```
using project quotas to measure disk usage
```

or

```
project quotas are not enabled on the filesystem of the data directory, counting files to measure disk usage
```

Project IDs start at 1850277888, well above the small IDs administrators usually use for their own
projects in `/etc/projid`.

### Counted differently

A project quota counts the space the files **take up on the disk**, like `du`. Counting files adds
up their **sizes**, like `du --apparent-size`. The two are usually close. Lots of small files take
up a little more than their size, because space is handed out in blocks of usually 4 KB, and sparse
files take up less.

### Enforcing the limit in the kernel

By default the quota is only used to measure, and the limit works as always: Wings stops a server
that goes over its disk limit. With `enforce: true`, the kernel itself refuses any write that would
take the server past its limit, the moment it happens. The server keeps running, and the game sees
"disk quota exceeded" errors instead of being stopped.

```yaml
system:
  disk_quota:
    mode: auto        # "auto": use project quotas when available; "off": always count files
    enforce: false    # true: the kernel refuses writes past the disk limit
```

Run `systemctl restart nycto-wings` after changing it.

### Turning project quotas on

This is done once, by whoever runs the node, and is entirely optional.

**On a new XFS disk** for the server data, mount it with `prjquota`, for example in `/etc/fstab`:

```
/dev/sdb1  /var/lib/nycto/volumes  xfs  defaults,prjquota  0 0
```

**On ext4**, the `project` and `quota` features have to be turned on while the filesystem is
**not mounted**, so it can't be done to the disk the system is running from without a rescue
system:

```bash
tune2fs -O project,quota /dev/sdb1
mount -o prjquota /dev/sdb1 /var/lib/nycto/volumes
```

**To try it without a spare disk**, use a file as a disk. This creates a 10 GB XFS "disk" for the
server data (stop the servers and Wings first, and move the existing folders onto it):

```bash
apt install -y xfsprogs
fallocate -l 10G /var/lib/nycto-volumes.img
mkfs.xfs /var/lib/nycto-volumes.img
mount -o loop,prjquota /var/lib/nycto-volumes.img /var/lib/nycto/volumes
```

### Developers

The kernel round-trip is tested by `go test ./internal/quota ./server/filesystem -run ProjectQuota`,
run as root with `NYCTO_QUOTA_TEST_DIR` set to a directory on a filesystem with project quotas.
Without it those tests are skipped.

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
With project quotas, the number shown is the space the files take up on the disk (see
[Counted differently](#counted-differently)).
