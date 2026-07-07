# NimoOS 核心服务详解

NimoOS 是整个系统的核心服务，负责文件管理、系统监控、硬件信息采集、网络存储、存储路径迁移、系统升级以及与其他微服务的协调。

---

## 核心职责

- 文件和目录的增删改查、上传下载（tus 断点续传 + 传统分片两套通道）
- 按用户角色/文件夹授权的路径访问控制（`pkg/utils/path_acl.go` + `route/v1/file.go` 的 `checkPathAccess`）
- 系统硬件信息采集（CPU、内存、磁盘、网络、GPU，基于 gopsutil + sysfs/nvidia-smi）
- Samba/SMB 连接与共享管理
- 云存储挂载（Dropbox、Google Drive、OneDrive，通过 rclone；Google Drive 支持用户自建 OAuth 凭据）
- 存储路径迁移（Docker 镜像根 / AppData / 用户数据目录在磁盘间搬迁，`service/migrate.go`）
- 系统 OTA 升级（RAUC A/B 分区 OS 镜像 + 应用包升级，`service/system.go`）
- 通过 MessageBus 发布系统事件（含 `nimoos:media:created` / `nimoos:media:deleted` 供 Photos 实时增删索引）
- ZeroTier 网络集成
- WebSocket SSH 终端

---

## 目录结构

```
NimoOS/
├── main.go              # 启动入口：路由挂载、5s 硬件广播 cron、事件类型注册
├── api/nimoos/          # OpenAPI 规范（openapi.yaml，V2 生成源）
├── common/              # 服务内常量：事件名（message.go）、tus 上传常量（upload.go）
├── route/
│   ├── init.go          # 开机初始化：网络挂载重放、磁盘休眠、path_config 自愈
│   ├── v1/              # V1 路由（文件、系统、Samba、云存储、驱动、ZeroTier 等）
│   ├── v2/              # V2 路由（OpenAPI 生成 + 手工挂载的 tus/precheck/uploads）
│   └── periodical.go    # 每 5s 广播硬件状态（CPU/内存/网络/GPU）
├── service/             # 业务逻辑层
│   ├── system.go        # 系统信息、GPU 状态、OTA 升级、下载器、迁移入口
│   ├── file.go          # 文件操作队列（复制/移动，带落盘校验）
│   ├── media_events.go  # nimoos:media:created 事件过滤与发布
│   ├── migrate.go       # 存储路径迁移引擎 + path_config.json
│   ├── user.go          # 只读查询 user.db：角色、文件夹授权（30s 缓存）
│   ├── upload/          # tus 上传任务 store、hook 映射、staging GC
│   ├── pathlock/        # 按路径读写锁，串行化同路径并发写
│   ├── notify.go        # 通知与事件发布
│   ├── storage.go       # rclone 云存储挂载
│   ├── connections.go   # SMB 连接管理（unix.Mount cifs）
│   └── shares.go        # Samba 共享配置
├── model/               # API 层数据模型
├── pkg/                 # 工具包（config、sqlite、cache、samba、utils/path_acl）
├── drivers/             # 云存储驱动（base/oauth.go 为统一 OAuth 中转端点）
└── build/               # 构建产物、systemd 服务文件
```

---

## API 版本

### V1（传统，JWT 验证，`route/v1.go`）

| 路由前缀 | 功能 |
|---|---|
| `/v1/sys/` | 系统信息、OS/应用版本检查与升级、下载取消、SSH、日志、硬件利用率、系统路径与迁移（`/paths`、`/migrate`）、磁盘休眠 |
| `/v1/file/` | 文件下载/创建/重命名/内容读写/传统分片上传/WebSocket 操作 |
| `/v1/folder/` | 目录列表、创建、重命名、大小与文件数统计 |
| `/v1/batch/` | 批量删除、文件移动/复制任务队列、打包下载 |
| `/v1/image/` | 图片原图/缩略图（`GetFileImage`） |
| `/v1/cloud/` | 云存储列表/卸载 |
| `/v1/driver/` | 云盘驱动列表、Google Drive BYO 授权（`POST /driver/google_drive/auth`） |
| `/v1/samba/` | SMB 连接和共享 CRUD |
| `/v1/notify/` | 推送通知 |
| `/v1/other/` | 文件搜索 |
| `/v1/zt/` | ZeroTier 本地 API 反代 |
| `/v1/recover/:type` | 云盘 OAuth 授权回调（免 JWT） |

### V2（OpenAPI 3.0，JWT 验证，`route/v2.go`）

| 路由前缀 | 功能 |
|---|---|
| `/v2/nimoos/health/` | 服务健康状态、端口占用、日志下载 |
| `/v2/nimoos/file/upload-tus` | tus 断点续传上传端点（tusd v2，手工挂载、跳过 OpenAPI 校验） |
| `/v2/nimoos/file/upload-precheck` | 重选续传预检：按目标路径+大小跳过已存在文件（`route/v2/precheck_file.go`） |
| `/v2/nimoos/file/uploads` | 上传任务列表/详情/取消（`route/v2/upload_tasks.go`） |
| `/v2/nimoos/local_storage/` | 磁盘显示名读写（手工注册，绕过截断的 codegen 接口） |
| `/v2/nimoos/zt/` | ZeroTier 信息/状态（OpenAPI 生成） |

> 旧的 V2 简单分片上传（UploadFile/TestChunk）已删除，OpenAPI spec 中不再有对应 path；文件上传主通道是 tus。

### V3（文件下载，`route/v2.go` 的 `InitFile`）

- `/v3/file?token=&path=` — 带 JWT token 验证的文件下载；`&inline=1` 时以 inline Disposition 返回，供浏览器内预览（PDF/图片，支持 Range 与 `#page=N` 跳页）

---

## 认证与路径权限

- **JWT 中间件**（`route/v1.go` / `route/v2.go`）：带 Authorization 头或 `?token=` 的请求**必须**解析 token（即使来自 localhost），解析后把 `user_id`、`user_name` 写入请求头；仅无任何凭证的 localhost 请求跳过验证。
- **实时角色**：V1 中间件每次请求经 `service/user.go` 的 `GetUserRoleByID` 直查 UserService 的 `user.db`（只读打开），把 `user_role` 写入请求头，避免 JWT 里的角色过期；查库失败时 fail-closed 降级为 `user`。
- **路径门禁**（`route/v1/file.go` 的 `checkPathAccess`，覆盖所有文件/目录 handler）：
  1. 无 `user_id`/`user_role` 头（真内部调用）直接放行；
  2. `user_id == 1` 的根管理员放行所有路径；
  3. 其余用户走 `pkg/utils/path_acl.go` 的 `IsPathAllowed` 基线（`/DATA`、`/mnt`、`/media` 等），再叠加 `user.db` 中 `user_folder_permissions` 表的显式文件夹授权（`IsPathGranted`，30s 缓存）。
- **系统目录保护**：`containsProtectedName`（`route/v1/file.go`、`route/v2/file.go` 各有一份）拦截对系统默认文件夹名的写入；但用户上传进 `Documents/Downloads/Gallery/Media` 等自身内容目录不受误拦，校验失败返回 4xx。

---

## 核心业务逻辑

### 文件上传（tus 主通道）

- 基于 NimoOS-Common 的共享上传引擎（`commonUpload`）+ tusd v2，分片先落 staging 目录 `/DATA/.system_data/file-tus-staging`（`common/upload.go`），完成后经 rename（跨设备回退为 copy）搬到目标路径（`route/v2/tus_file.go` 的 `ingestToTargetWithPolicy`，支持冲突策略与同名去重）。
- 创建时校验 metadata 与 `/DATA` 剩余空间配额；resumed 上传幂等落地，不产生重复文件。
- 每次 tus CreatedUploads 事件映射为 `o_upload_tasks` 表中一条任务行（`service/upload/hook.go`），供 `/file/uploads` 任务 API 查询/取消；6 小时无进展降级为 paused，staging 保留 3 天后由后台 GC 清理（`service/upload/gc.go`，`commonUpload.StartGC` 随 tus handler 启动）。
- 上传前可先调 `/file/upload-precheck` 按「目标路径 + 大小」跳过已存在文件，用于重选续传。
- V1 传统分片上传（`/v1/file/upload`，`route/v1/file.go`）仍保留兼容：分片写入 `.temp` 目录，齐片后同步 `SpliceFiles` 合并（经 `pathlock` 路径写锁），O_EXCL 保证分片幂等。

### 媒体事件（Photos 集成）

- **`nimoos:media:created`**（`common/message.go`，实现在 `service/media_events.go`）：文件真正落盘后发布，`properties["paths"]` 为 JSON 数组，元素可为文件或目录（整目录复制/移动只发目的地根）。发布点：tus 落地（`route/v2/tus_file.go`）、V1 分片/单发上传（`route/v1/file.go`）、批量复制/移动完成（`service/file.go`）。发布带 10s 超时、fire-and-forget——MessageBus 是软依赖，失败只记日志，绝不影响文件操作本身。
- **`nimoos:media:deleted`**（`route/v1/file.go` 的 `DeleteFile`）：删除成功后发布，只含图片/视频扩展名或疑似目录的路径，供 Photos 实时清理索引。
- 两处的媒体扩展名清单（`mediaCreatedExts` / `deletedMediaExts`）互为镜像，新增格式时需同步。

### 批量文件操作

- 复制/移动走任务队列（`service/file.go` 的 `FileQueue`），移动落地后校验源/目的大小一致才删源，不一致则回滚目的、保留源；copy+skip 策略下目的已存在时不发 media 事件。

### 系统硬件监控

- `main.go` 以 cron `@every 5s` 调 `route/periodical.go` 的 `SendAllHardwareStatusBySocket`，聚合 CPU（占用/核数/温度/功耗/厂商）、内存、网络、GPU，经 `Notify().SendNotify` 以 `nimoos:system:utilization` 事件广播；同一数据也可经 `/v1/sys/utilization` 拉取。
- **GPU**（`service/system.go` 的 `GetGpuStatus`）：先经 nvidia-smi 枚举 NVIDIA，再扫 `/sys/class/drm` 枚举 Intel（utilization 从 GT idle residency 差分估算、VRAM 从 debugfs 读取、温度经 hwmon、名称经 lspci），两路结果合并而非互相覆盖；有温度读数的卡排前面供前端主 widget 展示。`main.go` 启动 `external.StartIntelGpuMonitor()`（无 intel_gpu_top 时为 no-op）。
- **CPU 温度**：ACPI thermal zone 读数为 0 时回退 hwmon（`GetCPUHwmonPath`）。

### 系统升级（OTA）

- **OS 升级**：`/v1/sys/os_version/check` + `/v1/sys/os_update`。从 `ServerApi` 指向的更新服务器（校验平台/硬件兼容性与版本连续性）下载 `.raucb` A/B 分区镜像，`rauc install` 经 `systemd-run --scope --unit=nimoos-upgrade` 跑在独立 scope 中，服务自身重启不会连带杀死升级进程。
- **应用包升级**：`/v1/sys/version` + `/v1/sys/update`，同样的下载器 + 独立 scope（`nimoos-app-upgrade`）模式。
- 下载器带取消（`/v1/sys/download/cancel`）、进度查询与每日自动检查（`StartDailyDownloadChecker`）；重启后 `SyncStartupUpgradeStatus`（`main.go` 启动时调用）读取遗留状态文件，向云端回报升级成功/回滚并上传日志；`readRAUCBootStatus` 把 A/B 分区 boot 状态随版本检查上报。

### 存储路径迁移

- `service/migrate.go`：把 Docker 镜像根（images）、AppData、用户数据（Gallery/Downloads 等 UserData 组批量迁移）在磁盘间搬迁，当前配置持久化在 `/var/lib/nimoos/path_config.json`。
- 迁移期间：路径写锁 + 迁移锁文件防并发、停容器并上报进度（`/v1/sys/migrate/:id` 轮询）、复制后逐一校验再删源、循环软链防护（`GetFileOrDirSize` 防递归）；images 迁移会改写 `/etc/docker/daemon.json` 的 `data-root` 并等待 Docker 就绪后重启原容器。
- 开机 `route/init.go` 的 `InitPathConfig` 用 daemon.json 实际值自愈 path_config.json，并同步 `/var/lib/nimoos/docker_root` 供 AppManagement 读取。

### 云存储挂载

- 通过 rclone HTTP API 挂载/卸载远程存储（`service/storage.go`），配置存 rclone 自己的 config，开机 `InitNetworkMount` 末尾 `CheckAndMountAll` 重放。
- 所有云盘 OAuth 回调统一走中转域名 `https://cloudoauth.nimopc.com`（`drivers/base/oauth.go`），由中转页跳回本机 `/v1/recover/:type`。
- **Google Drive BYO**：`POST /v1/driver/google_drive/auth`（`route/v1/driver.go`）接收用户自建的 client_id/client_secret，凭据只存服务器内存短期缓存（10 分钟 TTL，随机 sid 为键），返回拼好的 Google 授权 URL；回调 `GetRecoverStorage` 按 state 中的 sid 取回凭据换 token，client_secret 绝不进入 URL/中转页。

### Samba 管理

- **客户端（连接远端共享）**：连接经 GORM + SQLite 持久化，`unix.Mount` 以 cifs 挂到 `/mnt/<host>/<share>`。创建连接（`route/v1/samba.go` 的 `PostSambaConnectionsCreate`）逐共享挂载：失败的（IPC$/print$ 或权限不足）清理空目录并跳过，只把挂载成功的写回 DB；**一个都没挂上时返回真实错误而非假 200**，且响应不回显明文密码。
- 开机重放（`route/init.go` 的 `InitNetworkMount`）与创建路径同款逐共享处理；清理残留挂载点时先 unmount，**卸不掉绝不递归删除**——`os.RemoveAll` 会穿透仍活跃的 CIFS 挂载点误删远端文件。
- **服务端（对外共享）**：共享按创建者用户名记录（`Anonymous` 仅在无用户名时为真，`service/model/o_shares.go`），生成 Samba 配置文件并调 shell 脚本管理 smbd。

---

## 数据库

- SQLite + GORM，主库位于 `DBPath` 下（`pkg/sqlite/db.go` AutoMigrate）
- 表：`o_connections`（SMB 连接）、`o_shares`（SMB 共享）、`o_notify`（通知记录）、`o_peer_drive`（局域网 peer 设备）、`o_upload_tasks`（tus 上传任务，模型在 NimoOS-Common/upload）
- 另以只读方式打开 UserService 的 `user.db` 查角色与文件夹授权（`service/user.go`），不写入
- 云存储配置不在本库，由 rclone config 管理；迁移路径状态在 `/var/lib/nimoos/path_config.json`

---

## 配置

`conf/conf.conf.sample`：

```ini
[app]
LogPath = /var/log/nimoos/
DBPath = /var/lib/nimoos
ShellPath = /usr/share/nimoos/shell
UserDataPath = /var/lib/nimoos/conf

[server]
ServerApi = https://api.nimoos.io/nimoos-api   # 版本检查/升级/日志上报服务器
USBAutoMount =

[common]
RuntimePath = /var/run/nimoos
```

---

## 依赖的其他服务

| 服务 | 用途 |
|---|---|
| NimoOS-MessageBus | 发布系统事件（硬件状态、文件操作、media created/deleted） |
| NimoOS-Gateway | 注册 API 路由、服务发现 |
| NimoOS-UserService | `user.db` 只读查询：用户角色、文件夹授权 |
| NimoOS-Common | JWT 验证、日志、HTTP 工具、共享上传引擎（`upload` 包） |
| NimoOS-Photos | 消费 media 事件维护相册索引（事件消费方） |
| rclone | 云存储挂载 |
| RAUC | A/B 分区 OS 镜像升级 |
| systemd | 服务生命周期管理、升级进程独立 scope |

---

## 技术栈

- **框架**：Echo v4
- **数据库**：SQLite + GORM
- **日志**：go.uber.org/zap
- **系统信息**：shirou/gopsutil
- **断点续传**：tus/tusd v2（经 NimoOS-Common upload 引擎）
- **实时通信**：gorilla/websocket、go-socket.io
- **挂载**：golang.org/x/sys/unix（cifs）、moby/sys/mount
- **定时任务**：robfig/cron v3
- **归档**：mholt/archiver v3
