# NimoOS Core Service Details

NimoOS is the core service of the whole system, responsible for file management, system monitoring, hardware info collection, network storage, storage path migration, system upgrades, and coordination with the other microservices.

---

## Core Responsibilities

- CRUD, upload/download for files and directories (tus resumable upload + legacy chunked upload, two channels)
- Path access control by user role / folder authorization (`pkg/utils/path_acl.go` + `checkPathAccess` in `route/v1/file.go`)
- System hardware info collection (CPU, memory, disk, network, GPU, based on gopsutil + sysfs/nvidia-smi)
- Samba/SMB connection and share management
- Cloud storage mounting (Dropbox, Google Drive, OneDrive, via rclone; Google Drive supports user-provided OAuth credentials)
- Storage path migration (moving the Docker image root / AppData / user data directory between disks, `service/migrate.go`)
- System OTA upgrades (RAUC A/B partition OS image + app package upgrades, `service/system.go`)
- Publishing system events via MessageBus (including `nimoos:media:created` / `nimoos:media:deleted` for Photos to update its index in real time)
- ZeroTier network integration
- WebSocket SSH terminal

---

## Directory Structure

```
NimoOS/
├── main.go              # Entry point: route mounting, 5s hardware broadcast cron, event type registration
├── api/nimoos/          # OpenAPI spec (openapi.yaml, V2 codegen source)
├── common/              # In-service constants: event names (message.go), tus upload constants (upload.go)
├── route/
│   ├── init.go          # Boot init: network mount replay, disk sleep, path_config self-healing
│   ├── v1/              # V1 routes (file, system, Samba, cloud storage, drivers, ZeroTier, etc.)
│   ├── v2/              # V2 routes (OpenAPI-generated + manually mounted tus/precheck/uploads)
│   └── periodical.go    # Broadcasts hardware status every 5s (CPU/memory/network/GPU)
├── service/             # Business logic layer
│   ├── system.go        # System info, GPU status, OTA upgrades, downloader, migration entry point
│   ├── file.go          # File operation queue (copy/move, with on-disk verification)
│   ├── media_events.go  # nimoos:media:created event filtering and publishing
│   ├── migrate.go       # Storage path migration engine + path_config.json
│   ├── user.go          # Read-only queries against user.db: roles, folder authorization (30s cache)
│   ├── upload/          # tus upload task store, hook mapping, staging GC
│   ├── pathlock/        # Per-path read/write locks, serializes concurrent writes to the same path
│   ├── notify.go        # Notifications and event publishing
│   ├── storage.go       # rclone cloud storage mounting
│   ├── connections.go   # SMB connection management (unix.Mount cifs)
│   └── shares.go        # Samba share configuration
├── model/               # API layer data models
├── pkg/                 # Utility packages (config, sqlite, cache, samba, utils/path_acl)
├── drivers/             # Cloud storage drivers (base/oauth.go is the unified OAuth relay endpoint)
└── build/               # Build artifacts, systemd service files
```

---

## API Versions

### V1 (legacy, JWT-verified, `route/v1.go`)

| Route prefix | Function |
|---|---|
| `/v1/sys/` | System info, OS/app version check & upgrade, download cancellation, SSH, logs, hardware utilization, system paths & migration (`/paths`, `/migrate`), disk sleep |
| `/v1/file/` | File download/create/rename/content read-write/legacy chunked upload/WebSocket ops |
| `/v1/folder/` | Directory listing, creation, renaming, size and file count stats |
| `/v1/batch/` | Batch delete, file move/copy task queue, packaged download |
| `/v1/image/` | Original/thumbnail images (`GetFileImage`) |
| `/v1/cloud/` | Cloud storage listing/unmounting |
| `/v1/driver/` | Cloud drive driver listing, Google Drive BYO auth (`POST /driver/google_drive/auth`) |
| `/v1/samba/` | SMB connection and share CRUD |
| `/v1/notify/` | Push notifications |
| `/v1/other/` | File search |
| `/v1/zt/` | ZeroTier local API reverse proxy |
| `/v1/recover/:type` | Cloud drive OAuth authorization callback (JWT-exempt) |

### V2 (OpenAPI 3.0, JWT-verified, `route/v2.go`)

| Route prefix | Function |
|---|---|
| `/v2/nimoos/health/` | Service health status, port usage, log download |
| `/v2/nimoos/file/upload-tus` | tus resumable upload endpoint (tusd v2, manually mounted, skips OpenAPI validation) |
| `/v2/nimoos/file/upload-precheck` | Precheck for resuming an upload with a different selection: skips files that already exist by target path + size (`route/v2/precheck_file.go`) |
| `/v2/nimoos/file/uploads` | Upload task listing/detail/cancellation (`route/v2/upload_tasks.go`) |
| `/v2/nimoos/local_storage/` | Disk display name read/write (manually registered, works around a truncated codegen interface) |
| `/v2/nimoos/zt/` | ZeroTier info/status (OpenAPI-generated) |

> The old V2 simple chunked upload (UploadFile/TestChunk) has been removed and no longer has a corresponding path in the OpenAPI spec; the primary upload channel is tus.

### V3 (file download, `InitFile` in `route/v2.go`)

- `/v3/file?token=&path=` — file download with JWT token verification; with `&inline=1`, returns with an inline Disposition for in-browser preview (PDF/images, supports Range and jumping to a page via `#page=N`)

---

## Auth and Path Permissions

- **JWT middleware** (`route/v1.go` / `route/v2.go`): a request carrying an Authorization header or `?token=` **must** have its token parsed (even from localhost); once parsed, `user_id` and `user_name` are written into the request headers. Only localhost requests with no credentials at all skip verification.
- **Live role lookup**: on every request, the V1 middleware calls `GetUserRoleByID` in `service/user.go` to query UserService's `user.db` directly (opened read-only), writing `user_role` into the request headers to avoid a stale role baked into the JWT; if the query fails, it fails closed and downgrades to `user`.
- **Path gating** (`checkPathAccess` in `route/v1/file.go`, covers all file/directory handlers):
  1. Requests with no `user_id`/`user_role` headers (genuine internal calls) pass straight through;
  2. The root admin (`user_id == 1`) is allowed on all paths;
  3. Other users go through the baseline `IsPathAllowed` check in `pkg/utils/path_acl.go` (`/DATA`, `/mnt`, `/media`, etc.), layered with explicit folder grants from the `user_folder_permissions` table in `user.db` (`IsPathGranted`, 30s cache).
- **System directory protection**: `containsProtectedName` (one copy each in `route/v1/file.go` and `route/v2/file.go`) blocks writes to system default folder names; but user uploads into their own content directories such as `Documents/Downloads/Gallery/Media` are not mistakenly blocked, and validation failures return a 4xx.

---

## Core Business Logic

### File Upload (tus primary channel)

- Built on NimoOS-Common's shared upload engine (`commonUpload`) + tusd v2. Chunks first land in the staging directory `/DATA/.system_data/file-tus-staging` (`common/upload.go`), then on completion are moved to the target path via rename (falling back to copy across devices) (`ingestToTargetWithPolicy` in `route/v2/tus_file.go`, supports conflict policies and dedup by name).
- On creation, metadata and remaining `/DATA` space quota are validated; resumed uploads are idempotent and never produce duplicate files.
- Each tus CreatedUploads event is mapped to a task row in the `o_upload_tasks` table (`service/upload/hook.go`), queryable/cancelable via the `/file/uploads` task API; a task with no progress for 6 hours is downgraded to paused, and staging is kept for 3 days before background GC cleans it up (`service/upload/gc.go`; `commonUpload.StartGC` starts alongside the tus handler).
- Before uploading, `/file/upload-precheck` can be called to skip files that already exist by "target path + size", for resuming with a different selection.
- The V1 legacy chunked upload (`/v1/file/upload`, `route/v1/file.go`) is still kept for compatibility: chunks are written to a `.temp` directory, and once all chunks are present, `SpliceFiles` merges them synchronously (under a `pathlock` write lock), with O_EXCL guaranteeing chunk idempotency.

### Media Events (Photos integration)

- **`nimoos:media:created`** (`common/message.go`, implemented in `service/media_events.go`): published after a file is actually written to disk; `properties["paths"]` is a JSON array whose elements can be files or directories (a whole-directory copy/move only publishes the destination root). Publish points: tus landing (`route/v2/tus_file.go`), V1 chunked/single-shot upload (`route/v1/file.go`), completed batch copy/move (`service/file.go`). Publishing has a 10s timeout and is fire-and-forget — MessageBus is a soft dependency, and failures are only logged, never affecting the file operation itself.
- **`nimoos:media:deleted`** (`DeleteFile` in `route/v1/file.go`): published after a successful delete, only including paths with image/video extensions or that look like directories, so Photos can clean up its index in real time.
- The media extension lists in the two places (`mediaCreatedExts` / `deletedMediaExts`) mirror each other and must be kept in sync when new formats are added.

### Batch File Operations

- Copy/move go through a task queue (`FileQueue` in `service/file.go`); after a move lands, the source is only deleted once source/destination sizes are verified to match — if they don't match, the destination is rolled back and the source kept; under the copy+skip policy, no media event is published when the destination already exists.

### System Hardware Monitoring

- `main.go` uses a `@every 5s` cron to call `SendAllHardwareStatusBySocket` in `route/periodical.go`, aggregating CPU (usage/core count/temperature/power/vendor), memory, network, and GPU, and broadcasts it via `Notify().SendNotify` as a `nimoos:system:utilization` event; the same data can also be pulled via `/v1/sys/utilization`.
- **GPU** (`GetGpuStatus` in `service/system.go`): first enumerates NVIDIA GPUs via nvidia-smi, then scans `/sys/class/drm` for Intel GPUs (utilization estimated from the GT idle residency delta, VRAM read from debugfs, temperature via hwmon, name via lspci); the two result sets are merged rather than one overriding the other, with cards that have a temperature reading sorted first for the frontend's main widget. `main.go` starts `external.StartIntelGpuMonitor()` (a no-op when intel_gpu_top is absent).
- **CPU temperature**: falls back to hwmon (`GetCPUHwmonPath`) when the ACPI thermal zone reading is 0.

### System Upgrade (OTA)

- **OS upgrade**: `/v1/sys/os_version/check` + `/v1/sys/os_update`. Downloads the `.raucb` A/B partition image from the update server pointed to by `ServerApi` (which validates platform/hardware compatibility and version continuity); `rauc install` runs in its own scope via `systemd-run --scope --unit=nimoos-upgrade`, so a restart of the service itself won't also kill the upgrade process.
- **App package upgrade**: `/v1/sys/version` + `/v1/sys/update`, using the same downloader + independent scope pattern (`nimoos-app-upgrade`).
- The downloader supports cancellation (`/v1/sys/download/cancel`), progress queries, and a daily auto-check (`StartDailyDownloadChecker`); after a restart, `SyncStartupUpgradeStatus` (called at `main.go` startup) reads the leftover status file, reports upgrade success/rollback to the cloud and uploads logs; `readRAUCBootStatus` reports the A/B partition boot status alongside version checks.

### Storage Path Migration

- `service/migrate.go`: moves the Docker image root (images), AppData, and user data (Gallery/Downloads etc., migrated in the UserData batch) between disks; current config is persisted in `/var/lib/nimoos/path_config.json`.
- During migration: a path write lock plus a migration lock file prevent concurrency; containers are stopped and progress is reported (polled via `/v1/sys/migrate/:id`); after copying, each item is verified before the source is deleted; there's a guard against circular symlinks (`GetFileOrDirSize` prevents recursion). An images migration rewrites the `data-root` field in `/etc/docker/daemon.json` and restarts the original containers once Docker is ready again.
- At boot, `InitPathConfig` in `route/init.go` self-heals `path_config.json` using the actual value from daemon.json, and syncs `/var/lib/nimoos/docker_root` for AppManagement to read.

### Cloud Storage Mounting

- Mounts/unmounts remote storage via the rclone HTTP API (`service/storage.go`); config is stored in rclone's own config file, and `CheckAndMountAll` at the end of `InitNetworkMount` replays it at boot.
- All cloud drive OAuth callbacks go through the unified relay domain `https://cloudoauth.nimotech.ai` (`drivers/base/oauth.go`), which redirects back to the local `/v1/recover/:type`.
- **Google Drive BYO**: `POST /v1/driver/google_drive/auth` (`route/v1/driver.go`) accepts a user-supplied client_id/client_secret; the credentials are only cached briefly in server memory (10-minute TTL, keyed by a random sid), and a ready-made Google authorization URL is returned. The callback `GetRecoverStorage` retrieves the credentials by the sid in the state to exchange for a token — the client_secret never enters the URL or the relay page.

### Samba Management

- **Client (connecting to remote shares)**: connections are persisted via GORM + SQLite, and `unix.Mount` mounts them as cifs under `/mnt/<host>/<share>`. Creating a connection (`PostSambaConnectionsCreate` in `route/v1/samba.go`) mounts each share individually: shares that fail (IPC$/print$ or insufficient permissions) have their empty directory cleaned up and are skipped, and only successfully mounted shares are written back to the DB; **if none of them mount, a real error is returned instead of a fake 200**, and the response never echoes back the plaintext password.
- Boot-time replay (`InitNetworkMount` in `route/init.go`) uses the same per-share handling as the creation path; when cleaning up leftover mount points, it unmounts first and **never recursively deletes if the unmount fails** — `os.RemoveAll` would traverse into a still-active CIFS mount point and delete remote files by mistake.
- **Server (sharing outward)**: shares are recorded by the creator's username (`Anonymous` is only true when there's no username, `service/model/o_shares.go`); a Samba config file is generated and a shell script is invoked to manage smbd.

---

## Database

- SQLite + GORM, main database located under `DBPath` (AutoMigrate in `pkg/sqlite/db.go`)
- Tables: `o_connections` (SMB connections), `o_shares` (SMB shares), `o_notify` (notification records), `o_peer_drive` (LAN peer devices), `o_upload_tasks` (tus upload tasks, model in NimoOS-Common/upload)
- Also opens UserService's `user.db` read-only to query roles and folder authorization (`service/user.go`), never writes to it
- Cloud storage config is not in this database and is managed by rclone's config; migration path state lives in `/var/lib/nimoos/path_config.json`

---

## Configuration

`conf/conf.conf.sample`:

```ini
[app]
LogPath = /var/log/nimoos/
DBPath = /var/lib/nimoos
ShellPath = /usr/share/nimoos/shell
UserDataPath = /var/lib/nimoos/conf

[server]
ServerApi =                                 # Version check/handshake service; NimoOS does not run this service, leave blank by default to stay offline
USBAutoMount =

[common]
RuntimePath = /var/run/nimoos
```

---

## Dependencies on Other Services

| Service | Purpose |
|---|---|
| NimoOS-MessageBus | Publishes system events (hardware status, file operations, media created/deleted) |
| NimoOS-Gateway | Registers API routes, service discovery |
| NimoOS-UserService | Read-only queries against `user.db`: user roles, folder authorization |
| NimoOS-Common | JWT verification, logging, HTTP utilities, shared upload engine (`upload` package) |
| NimoOS-Photos | Consumes media events to maintain the photo album index (event consumer) |
| rclone | Cloud storage mounting |
| RAUC | A/B partition OS image upgrades |
| systemd | Service lifecycle management, upgrade process runs in its own scope |

---

## Tech Stack

- **Framework**: Echo v4
- **Database**: SQLite + GORM
- **Logging**: go.uber.org/zap
- **System info**: shirou/gopsutil
- **Resumable upload**: tus/tusd v2 (via the NimoOS-Common upload engine)
- **Real-time communication**: gorilla/websocket, go-socket.io
- **Mounting**: golang.org/x/sys/unix (cifs), moby/sys/mount
- **Scheduled tasks**: robfig/cron v3
- **Archiving**: mholt/archiver v3
