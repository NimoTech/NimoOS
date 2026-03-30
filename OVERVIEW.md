# CasaOS 核心服务详解

CasaOS 是整个系统的核心服务，负责文件管理、系统监控、硬件信息采集、网络存储以及与其他微服务的协调。

---

## 核心职责

- 文件和目录的增删改查、上传下载（支持断点续传）
- 系统硬件信息采集（CPU、内存、磁盘、网络，基于 gopsutil）
- Samba/SMB 连接与共享管理
- 云存储挂载（Dropbox、Google Drive、OneDrive，通过 rclone）
- ZeroTier 网络集成
- 通过 MessageBus 发布系统事件
- WebSocket SSH 终端
- 系统升级与维护

---

## 目录结构

```
CasaOS/
├── main.go              # 启动入口
├── api/                 # OpenAPI 规范
├── route/
│   ├── v1/              # V1 路由（文件、系统、Samba、云存储等）
│   ├── v2/              # V2 路由（OpenAPI 生成，含健康检查、文件上传）
│   └── periodical.go    # 定时广播硬件状态（WebSocket）
├── service/             # 业务逻辑层
│   ├── system.go        # 系统信息、网络、目录操作
│   ├── file.go          # 文件操作队列
│   ├── file_upload.go   # 分片上传管理
│   ├── notify.go        # 通知与事件发布
│   ├── storage.go       # rclone 云存储挂载
│   ├── connections.go   # SMB 连接管理
│   └── shares.go        # Samba 共享配置
├── model/               # 数据模型
├── pkg/                 # 工具包（config、sqlite、cache、samba）
├── drivers/             # 云存储驱动（Dropbox、Google Drive、OneDrive）
└── build/               # 构建产物、systemd 服务文件
```

---

## API 版本

### V1（传统，JWT 验证）

| 路由前缀 | 功能 |
|---|---|
| `/v1/sys/` | 系统信息、版本更新、SSH、日志、硬件利用率 |
| `/v1/file/` | 文件上传/下载/重命名/内容读写/WebSocket 操作 |
| `/v1/folder/` | 目录列表、创建、重命名、大小统计 |
| `/v1/batch/` | 批量删除、文件移动/复制任务队列 |
| `/v1/cloud/` | 云存储挂载/卸载 |
| `/v1/samba/` | SMB 连接和共享 CRUD |
| `/v1/notify/` | 推送通知 |
| `/v1/other/` | 文件搜索 |

### V2（OpenAPI 3.0，JWT 验证）

| 路由前缀 | 功能 |
|---|---|
| `/v2/casaos/health/` | 服务健康状态、端口占用、日志下载 |
| `/v2/casaos/file/` | 文件分片上传校验与提交 |

### V3（文件下载）

- `/v3/file?token=&path=` — 带 JWT token 验证的文件下载

---

## 核心业务逻辑

### 分片文件上传

1. 客户端分片上传（支持查询已上传的分片）
2. 每片写入 `.tmp` 临时文件
3. 全部分片完成后原子重命名为目标文件

### 系统硬件监控

- 每 5 秒通过 cron 广播 CPU/内存/磁盘/网络状态
- 通过 MessageBus 事件分发给订阅方（UI、其他服务）

### 云存储挂载

- 通过 rclone HTTP API 挂载/卸载远程存储
- 支持 Dropbox、Google Drive、OneDrive 驱动

### Samba 管理

- 连接通过 GORM + SQLite 持久化
- 通过 `unix.Mount` 系统调用挂载 SMB 共享
- 共享配置生成 Samba 配置文件，调用 shell 脚本管理服务

---

## 数据库

- SQLite + GORM
- 表：`connections`（SMB 连接）、`shares`（SMB 共享）、`storages`（云存储配置）、`app_notify`（通知记录）

---

## 配置

```ini
[app]
LogPath = /var/log/casaos/
DBPath = /var/lib/casaos
ShellPath = /usr/share/casaos/shell

[server]
ServerApi = https://api.casaos.io/casaos-api

[common]
RuntimePath = /var/run/casaos
```

---

## 依赖的其他服务

| 服务 | 用途 |
|---|---|
| CasaOS-MessageBus | 发布系统事件（文件操作、系统状态） |
| CasaOS-Gateway | 注册 API 路由、服务发现 |
| CasaOS-Common | JWT 验证、日志、HTTP 工具 |
| rclone | 云存储挂载 |
| systemd | 服务生命周期管理 |

---

## 技术栈

- **框架**：Echo v4
- **数据库**：SQLite + GORM
- **日志**：go.uber.org/zap
- **系统信息**：shirou/gopsutil
- **实时通信**：gorilla/websocket、go-socket.io
- **定时任务**：robfig/cron v3
- **归档**：mholt/archiver v3
