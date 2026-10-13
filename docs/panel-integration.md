# Building a panel for Nycto Wings

*For panel developers. Written for Nycto Wings v0.1.0.*

This guide explains what a panel has to do to run servers on Nycto Wings: what to build first, what
each endpoint must return, how to sign tokens, and the details that are easy to get wrong.
[`protocol/WINGS_API.md`](../protocol/WINGS_API.md) is the complete reference, and this guide links
to its sections (§) for exact details.

Nycto Wings speaks the same protocol as Pterodactyl Wings, plus optional additions (§10). A panel
that works with Pterodactyl Wings works with Nycto Wings, and the additions are there when you want them.

## How the two talk

There's a node token (`token_id` + `token`), and traffic goes both ways:

```
            Bearer <token_id>.<token>
  Wings ───────────────────────────────────▶ Panel   /api/remote/*      (§2)
        ◀───────────────────────────────────
            Bearer <token>                          /api/servers/*  …  (§3)
```

- **Wings → panel:** `Authorization: Bearer <token_id>.<token>`. Look up the node by `token_id` and
  compare the token in constant time. Wings also sends `Accept: application/vnd.pterodactyl.v1+json`
  and `User-Agent: Pterodactyl Wings/v<version> (id:<token_id>)`.
- **Panel → Wings:** `Authorization: Bearer <token>`: only the secret token, without the ID (§1.2).
- **Browsers → Wings:** the console websocket and file downloads and uploads don't go through the
  panel. The panel gives the browser a **JWT** it signs with the node's `token` (§5).

Wings listens on `api.port` (default 8080, often 443 or 8443 behind TLS) for HTTP, and on
`system.sftp.bind_port` (default 2022) for SFTP.

## 1. Registering a node

The panel creates a node with:
- a **UUID**
- a **`token_id`**: public, at least 16 random characters is fine
- a **`token`**: secret, 64 random characters

The `token` signs every JWT for that node, so treat it like a password.

The admin then runs on the node:

```bash
nycto-wings configure --panel-url https://panel.example.com --token <application API key> --node <node id>
```

That calls the panel's **`GET /api/application/nodes/<id>/configuration`** with
`Authorization: Bearer <application API key>` and `Accept: application/vnd.pterodactyl.v1+json`,
and expects `200` with the node's configuration as JSON. `401` or `403` means a wrong key. Wings
only takes these fields from it, and keeps everything else in its config file (`cmd/configure.go`):

```json
{
  "debug": false,
  "uuid": "<node uuid>",
  "token_id": "<token id>",
  "token": "<token>",
  "api": {
    "host": "0.0.0.0",
    "port": 8080,
    "ssl": { "enabled": true, "cert": "/etc/letsencrypt/live/node.example.com/fullchain.pem", "key": "/etc/letsencrypt/live/node.example.com/privkey.pem" },
    "upload_limit": 100
  },
  "system": { "sftp": { "bind_port": 2022 } }
}
```

The panel URL comes from `--panel-url`. Tokens sent this way can't use `file://` or `$VAR`
indirection. If your panel offers a "copy this YAML" box instead, the same keys in YAML plus
`remote: <panel url>` work.

Later changes to these fields can be pushed with `POST /api/update` (§3.3). It takes the same
object, ignores `ssl.cert` / `ssl.key`, and returns `{"applied": true}`.

## 2. The minimum to get a server running

Build these first, in this order.

### Endpoints the panel must serve, under `/api/remote`

| Endpoint | Return | Used when |
|---|---|---|
| `GET /servers?page=N&per_page=M` | `{"data": [{"uuid", "settings", "process_configuration"}], "meta": {"current_page", "last_page", ...}}` | Wings starts. Pages count from 1. **The first request uses `page=0`: answer it as page 1, with `"current_page": 1`.** Wings then fetches pages `current_page+1` to `last_page` in parallel |
| `GET /servers/{uuid}` | `{"settings": {...}, "process_configuration": {...}}` | Before every start, on `POST /api/servers/{uuid}/sync`, and when a server is created. `404` means it doesn't exist |
| `GET /servers/{uuid}/install` | `{"container_image", "entrypoint", "script"}` | Before installing |
| `POST /servers/{uuid}/install` | any `2xx` | After installing: `{"successful": bool, "reinstall": bool}` |
| `POST /servers/reset` | any `2xx` | Once at start-up, body `null`: mark servers stuck "installing" or "restoring" as normal |
| `POST /sftp/auth` | see [SFTP](#6-sftp) | Every SFTP login |
| `POST /activity` | any `2xx` | Every minute, with activity to log (§2 #12) |

Errors: answer with `{"errors": [{"code": "...", "status": "404", "detail": "..."}]}`. Wings retries
`5xx` and network errors for up to about 30 seconds, but never retries a `4xx`.

### `settings`: the server

```jsonc
{
  "uuid": "<server uuid>",                  // must match; Wings refuses a different one
  "meta": { "name": "My Server", "description": "" },
  "suspended": false,
  "invocation": "java -Xms128M -Xmx2048M -jar server.jar",  // the startup command, variables already filled in
  "skip_egg_scripts": false,
  "environment": { "SERVER_JARFILE": "server.jar", "VERSION": "1.21" },  // send values as strings
  "labels": {},
  "allocations": {
    "force_outgoing_ip": false,
    "default": { "ip": "0.0.0.0", "port": 25565 },
    "mappings": { "0.0.0.0": [25565, 25575] }
  },
  "build": {
    "memory_limit": 2048, "swap": 0, "io_weight": 500, "cpu_limit": 200,
    "disk_space": 10240, "threads": "", "oom_disabled": true
  },
  "crash_detection_enabled": true,
  "mounts": [],
  "egg": { "id": "<egg uuid>", "file_denylist": [] },
  "container": { "image": "ghcr.io/ptero-eggs/yolks:java_21" },
  "health_check": { "type": "tcp" }          // optional Nycto addition, §10.1
}
```

- **Units:** `memory_limit` and `disk_space` are MiB, `cpu_limit` is a percentage (200 = two
  cores), and 0 means unlimited.
- **Environment:** every value in `environment` becomes an environment variable. Send strings: a
  number like `25565` sent as a JSON number reaches the server as `25565.000000`.
- **Variables Wings sets itself:** Wings adds `STARTUP` (the `invocation`), `SERVER_MEMORY`,
  `SERVER_IP`, `SERVER_PORT` and `TZ`. A variable in `environment` with one of those names is ignored.
- **Inside the container:** the files are at `/home/container`. The image's entrypoint is expected
  to run `$STARTUP`, as the Pterodactyl "yolks" images do.
- **Ports:** every port in `mappings` is published on TCP and UDP. An IP of `127.0.0.1` is
  published on the node's Docker bridge instead, so servers can reach it.

### `process_configuration`: the egg's behaviour

```jsonc
{
  "startup": {
    "done": ["Done (", "regex:^\\[.*\\] Server started"],  // console line that means "started"
    "user_interaction": [],
    "strip_ansi": false
  },
  "stop": { "type": "command", "value": "stop" },          // "command", "signal" (e.g. "SIGINT") or "stop" (docker stop)
  "configs": [
    {
      "file": "server.properties",
      "parser": "properties",                              // file, yaml, properties, ini, json, xml
      "replace": [
        { "match": "server-port", "replace_with": "25565" },
        { "match": "server-ip", "replace_with": "0.0.0.0" }
      ]
    }
  ]
}
```

Before each start, Wings rewrites the listed config files. **Fill in every placeholder in
`replace_with` before sending**, for example the server's port. The only placeholder Wings resolves
itself is `{{config.docker.interface}}`, which is the node's Docker bridge IP.

### Installing

`GET /servers/{uuid}/install` returns:

```json
{ "container_image": "ghcr.io/ptero-eggs/installers:debian", "entrypoint": "bash", "script": "#!/bin/bash\n..." }
```

Wings runs `<entrypoint> /mnt/install/install.sh` in that image, with the server's files at
`/mnt/server` and the same environment variables. It then reports the result with
`POST /servers/{uuid}/install`. With `skip_egg_scripts: true` it reports success without running it.

### Creating, controlling and deleting servers (panel → Wings)

| Call | Body | Effect |
|---|---|---|
| `POST /api/servers` | `{"uuid": "<uuid v4>", "start_on_completion": true}` | Wings fetches `GET /servers/{uuid}` from you and installs. Returns `202` |
| `POST /api/servers/{uuid}/power` | `{"action": "start\|stop\|restart\|kill"}` | Returns `202`. The result arrives over the websocket |
| `POST /api/servers/{uuid}/sync` | – | Re-reads the settings from you. **Call it after changing a server's settings** |
| `POST /api/servers/{uuid}/commands` | `{"commands": ["say hi"]}` | Sends console commands |
| `POST /api/servers/{uuid}/reinstall` | – | Reinstalls |
| `DELETE /api/servers/{uuid}` | – | Deletes the server and its files |
| `GET /api/servers/{uuid}` | – | `{"state", "is_suspended", "utilization", "configuration", "health", "last_crash"}` |

The full list, including files, backups and transfers, is in §3.

## 3. Signing JWTs

Every JWT for a node is **HS256, signed with that node's `token`**. Wings checks:
- the signature and `exp`
- `scope`
- `iat`, `server_uuid` and `user_uuid`: tokens issued before Wings last started, or before a user's
  access was revoked, are refused
- the claims specific to each kind of token

It doesn't check `iss`, `aud` or `nbf`, but set them like Pterodactyl does.

| Token | `scope` | Extra claims | Lifetime (Pterodactyl) | Used at |
|---|---|---|---|---|
| Console | `websocket` | `permissions: []` | 10 min | websocket `auth` event |
| File download | `file-download` | `file_path`, `unique_id` | 15 min | `GET /download/file?token=` |
| File upload | `file-upload` | `unique_id` | 15 min | `POST /upload/file?token=&directory=` |
| Backup download | `backup-download` | `backup_uuid`, `unique_id` | 15 min | `GET /download/backup?token=` |
| Transfer | `transfer` | `sub` = server UUID | short | `POST /api/transfers` on the **destination** node |

Every token also needs `iat`, `exp`, `server_uuid` and `user_uuid` (except transfer).
`unique_id` makes download and upload tokens single-use, so use a fresh random value each time.
Example payload:

```json
{
  "iss": "https://panel.example.com",
  "aud": ["https://node.example.com:8080"],
  "jti": "<an ID that is the same for this user and server, e.g. 64 hex characters>",
  "iat": 1791842451, "nbf": 1791842151, "exp": 1791843051,
  "scope": "websocket",
  "server_uuid": "<server uuid>",
  "user_uuid": "<user uuid>",
  "permissions": ["control.console", "control.start", "control.stop", "control.restart", "websocket.connect"]
}
```

The payloads Pterodactyl 1.15.1 actually sends are in §5.4.

**Revoking access:** when a user loses access to servers, call
`POST /api/deauthorize-user` `{"user": "<user uuid>", "servers": ["<uuid>", ...]}`. An empty
`servers` list means all servers. Wings refuses that user's existing tokens, and closes their
console and SFTP sessions.

## 4. The console websocket

The browser connects to `wss://<node>:<port>/api/servers/<uuid>/ws`. Its `Origin` header must be
the panel URL, or be listed in the node's `allowed_origins`. Messages are
`{"event": "...", "args": ["..."]}`. Every argument is a string, and JSON is sent as a string.

1. Connect, then send `{"event": "auth", "args": ["<console JWT>"]}` within 30 seconds.
2. Wings replies with `auth success`, then the current `status`, and `health` (§10.1). If the server
   is offline because of a crash, it also sends `crash` (§10.2).
3. Send `send logs` for recent output, `send command` for a command, and `set state` with
   `start` / `stop` / `restart` / `kill` for power actions.
4. Wings sends `console output`, `status`, `stats` (about once a second while running), `install output`,
   `backup completed`, `transfer logs` and the rest of §4.5.
5. About a minute before the token expires, Wings sends `token expiring`. Get a new token from the
   panel and send `auth` again on the same socket.

**Permissions** in the console token:

| Permission | Allows |
|---|---|
| `websocket.connect` | Connecting at all. **Required** |
| `control.console` | Sending commands |
| `control.start`, `control.stop`, `control.restart` | Power actions. `kill` needs `control.stop` |
| `backup.read` | Receiving `backup completed` events |
| `admin.websocket.errors` | Seeing real error messages instead of a generic one |
| `admin.websocket.install` | Receiving install output |
| `admin.websocket.transfer` | Receiving transfer logs |

`"*"` grants everything except the `admin.*` permissions, which have to be listed explicitly.

## 5. Files and backups

- **Browsing and editing files:** call Wings from the panel with the node token:
  `GET /api/servers/{uuid}/files/list-directory?directory=/`, `GET .../files/contents?file=`,
  `POST .../files/write?file=` with the raw content, and the rest of §3.4.
- **Large downloads and uploads go browser → Wings directly:** give the browser
  `https://<node>:<port>/download/file?token=<file-download JWT>`, or
  `https://<node>:<port>/upload/file?token=<file-upload JWT>&directory=/`. Uploads are
  `multipart/form-data`, with each file in a part named `files`.
- **Backups:** `POST /api/servers/{uuid}/backup` `{"adapter": "wings", "uuid": "<backup uuid>", "ignore": ""}`.
  Wings creates it and reports back to you with `POST /api/remote/backups/{uuid}` (checksum and size).
  For S3, also implement `GET /api/remote/backups/{uuid}?size=`, which returns presigned upload URLs (§2).

## 6. SFTP

The SFTP username must look like `<anything>.<8 characters>`, for example `alice.9568b9c8`.
Pterodactyl uses the panel username plus the first 8 characters of the server UUID. Wings refuses
other usernames without asking the panel.

For every login, Wings calls `POST /api/remote/sftp/auth` with:

```json
{ "type": "password", "username": "alice.9568b9c8", "password": "<what they typed>", "ip": "203.0.113.5:51234", "session_id": "<base64>", "client_version": "<base64>" }
```

For key logins, `type` is `public_key` and `password` holds the key in `authorized_keys` format.
Answer `200` with:

```json
{ "server": "<server uuid>", "user": "<user uuid>", "permissions": ["file.read", "file.read-content", "file.create", "file.update", "file.delete"] }
```

Use `"*"` for every permission. Any `4xx` means wrong credentials. Throttle failed logins on your
side: Wings asks you about every attempt.

## 7. Nycto additions (optional)

All optional. A panel that ignores them still works.

| Addition | The panel sends | The panel reads | § |
|---|---|---|---|
| **Health checks** | `settings.health_check`, e.g. `{"type": "tcp"}` or `{"type": "http", "path": "/health"}`, ideally per egg | `health` websocket event, `health` in server details: `starting` / `healthy` / `unhealthy` with a message | 10.1 |
| **Crash explanations** | – | `crash` websocket event and `last_crash`: reason, a ready-made message ("ran out of memory (2 GB limit)"), the last log lines, and whether it's restarting | 10.2 |
| **Graphs** | – | `GET /api/servers/{uuid}/metrics?range=24h`: up to a week of CPU, memory, disk and network points | 10.3 |
| **Monitoring** | – | `GET /metrics` in the Prometheus format, for admins | 10.3 |

Suggested UI:
- a health badge next to the server status, with the message as a tooltip
- a crash banner with the message and an expandable "last logs" section
- a "Crash loop: fix the problem, then start the server again" banner when `reason_not_restarting`
  is `crash_loop`
- CPU, memory and network graphs, with 1h / 24h / 7d switches

## 8. Things that are easy to get wrong

| Mistake | What happens |
|---|---|
| `GET /servers?page=0` treated as an error | Wings can't load any servers at start-up |
| Answering `page=0` with `"current_page": 0` | Wings fetches the first page twice and logs duplicate servers |
| A token without `iat`, `server_uuid` or `user_uuid` | Wings refuses it. Console, downloads and uploads fail with "jwt" errors |
| Forgetting `scope` in a JWT | Same: every token is refused |
| Signing JWTs with `token_id`, or with `token_id.token` | Refused. Sign with `token` alone |
| Changing settings without calling `POST /api/servers/{uuid}/sync` | Wings uses the old settings until the next start |
| Unresolved placeholders in `replace_with` | The literal text ends up in the server's config file |
| Environment values sent as numbers | `25565.000000` in the server's environment |
| Reusing a download or upload token | Refused the second time: `unique_id` is single-use |
| Starting a server Wings marked as a crash loop | Fine, and the right thing to do: a user's start clears the crash history |
| `Origin` of the panel's website not matching the node's `remote` or `allowed_origins` | The browser's websocket connection is refused |

## 9. Testing

Point a test node at your panel and run Wings in the foreground with debug logging. It logs every
request it makes to the panel (with the token hidden) and every route it serves:

```bash
systemctl stop nycto-wings
nycto-wings --debug --config /etc/nycto/config.yml
```

`docs/` has a guide for each Nycto feature, and `protocol/WINGS_API.md` §8 lists what has been
tested against Pterodactyl panel 1.15.1. Those payloads are a working reference.
