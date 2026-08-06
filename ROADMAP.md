# NimoOS Roadmap

`✅ Shipped` · `🚧 In progress` · `📋 Planned` · `💭 Exploring`

Current line: `v1.9.x-alpha`

---

## Multi-user & permissions

| Capability | Status | Notes |
|---|---|---|
| User accounts and roles (admin / user) | ✅ | Includes Linux system user mapping, shadow passwords, per-user data directories |
| Unified folder permission panel | ✅ | Authorised directories, exclusions, per-subsystem watch scope |
| **Per-user scoping for the photo library** | 📋 | The library is global today — see [SECURITY.md](./SECURITY.md#known-limitations) |
| **Per-user filtering for the search index** | 📋 | The filename index is global today — see [SECURITY.md](./SECURITY.md#known-limitations) |
| Consolidated service-to-service identity chain | 💭 | Shared prerequisite for the two items above |

## Core

| Capability | Status | Notes |
|---|---|---|
| Single gateway entry point with dynamic route registration | ✅ | Microservices bind localhost only |
| Service discovery | ✅ | Runtime address files plus a Unix socket |
| JWT authentication (ECDSA P-256) with JWKS | ✅ | |
| Event bus (pub/sub over WebSocket) | ✅ | App install progress, disk hot-plug, hardware status |
| Component version and health view | ✅ | 12 services plus the UI and external dependencies |
| System monitoring | ✅ | CPU, memory, disk, network |

## Files & storage

| Capability | Status | Notes |
|---|---|---|
| File management (browse, upload, download, share) | ✅ | |
| Snapshots and a Time Machine view | ✅ | |
| Samba shares | ✅ | |
| Cloud storage mounts | ✅ | Backed by rclone |
| Disk management and USB auto-mount | ✅ | |
| RAID management | ✅ | |
| Rate-limited background indexing for large directories | ✅ | Keeps indexing from overwhelming low-power hardware |
| Rename fast path for same-volume moves | 📋 | Cross-directory moves currently copy then delete, which is slow for large files |
| Unified file-watch layer | 📋 | Wiki, Photos and Search each hold their own inotify watches today, multiplying the quota |
| Merged volumes (MergerFS) | 💭 | Code retained but disabled by default |

## Apps & containers

| Capability | Status | Notes |
|---|---|---|
| Docker Compose app lifecycle | ✅ | Install, start/stop, update, uninstall |
| Built-in app store | ✅ | |
| Third-party app store compatibility | ✅ | Accepts the `x-casaos` compose extension |

## AI agent

| Capability | Status | Notes |
|---|---|---|
| Local and cloud inference routing | ✅ | Multiple providers and models |
| Agent tooling | ✅ | File read/write, shell, bulk file structure, document reading including visual pages |
| Cross-session memory | ✅ | Profile layer, recall layer, automatic extraction, context compaction |
| Progressive disclosure for skills | ✅ | |
| MCP client | ✅ | Connects to external MCP servers |
| MCP server | ✅ | Exposes NimoOS capabilities to external AI clients (read-only tool set) |
| Chat platform bridges | ✅ | Telegram, Discord |
| Agent security model | ✅ | Behavioural guardrails, egress chokepoint, audit log — boundaries in [SECURITY.md](./SECURITY.md#ai-agent-security-model) |
| In-conversation model switching | 📋 | |
| More chat platforms | 💭 | WhatsApp |

## Knowledge & search

| Capability | Status | Notes |
|---|---|---|
| Global search | ✅ | Filename index plus vector recall, aggregated across sources |
| Document parsing and embedding | ✅ | Built on docling, written to a vector store |
| Wiki navigation map | ✅ | Maintains `.wiki.md` inside your storage, usable as long-term memory |
| Knowledge notes | ✅ | Manual capture plus automatic distillation from documents |
| Knowledge home | ✅ | |

## Photos

| Capability | Status | Notes |
|---|---|---|
| EXIF parsing and thumbnails | ✅ | |
| Local vector search | ✅ | Natural-language photo search |
| Image captioning | ✅ | Local vision model |
| Per-user scoping | 📋 | See Multi-user & permissions above |

## Terminal

| Capability | Status | Notes |
|---|---|---|
| Built-in web terminal | ✅ | Persistent tmux sessions, administrators only |
| Step-up password prompt, idle auto-lock, multiple window tabs | ✅ | |

## Platform

| Capability | Status | Notes |
|---|---|---|
| One-line installer | ✅ | |
| Offline dependency mirror | ✅ | Zero-network deployment on air-gapped or weak-network sites |
| Multi-architecture releases | ✅ | amd64, arm64, armv7 |
| LAN device discovery | ✅ | NimoOS instances find each other on the same subnet from the browser |
| Localised interface | ✅ | 33 languages |

## UI

| Capability | Status | Notes |
|---|---|---|
| Vue 2 panel | ✅ | The current default interface |
| Vue 3 rewrite | 🚧 | The new interface repository will be opened once the migration lands |

## Separate components

| Capability | Status | Notes |
|---|---|---|
| KVM virtual machine management | ✅ | Lives in its own repository, [NimoOS-KVM](https://github.com/NimoTech/NimoOS-KVM) |
