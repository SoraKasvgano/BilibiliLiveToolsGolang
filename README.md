# BilibiliLiveToolsGover

Go rewrite of BilibiliLiveTools (backend + frontend), focused on Bilibili live streaming orchestration with FFmpeg.

## 1. 项目简介

`BilibiliLiveToolsGover` 是对原 C# 工具[BilibiliLiveTools](https://github.com/withsalt/BilibiliLiveTools)的 Go 重写版本，侧重点：

- 尽量保持原功能 1:1 行为兼容；
- 扩展 RTSP / MJPEG / ONVIF PTZ 场景；
- 支持多路拼屏推流（拖拽排布、主画面优先、标题叠加）；
- 支持弹幕控制 PTZ、机器人命令、Webhook 编排能力；
- 使用纯 Go 依赖（SQLite 使用 `modernc.org/sqlite`，避免 cgo 依赖）；
- 前端资源使用 `embed` 自动嵌入到二进制。

## 2. 当前主要能力

- 推流输入：视频、USB 摄像头、RTSP、MJPEG、桌面、ONVIF（PTZ联动）。
- 摄像头资产库：支持 RTSP/MJPEG/ONVIF/USB 统一管理，ONVIF 每台设备可独立用户名/密码（明文存储），并可自动探测并回填 RTSP 地址。
- GB28181 平台接入：支持 SIP 注册、Digest 鉴权、Keepalive、Catalog 目录、INVITE/BYE、会话落库与状态维护（含 ACK、会话超时兜底、重邀）。
- GB28181 推流接入桥：支持按会话导出 SDP 到本地文件，并一键生成/更新 `gb28181` 摄像头源（可选自动套用推流配置）。
- GB28181 端口池：支持媒体端口池（start/end）分配，降低多路并发冲突概率。
- Bilibili 能力：登录状态、二维码登录、Cookie 刷新、开播/关播、房间信息管理。
- Bilibili 错误容错：重试、错误分级、完整响应落库、索引/详情查询。
- 集成能力：Webhook / Bot 异步任务队列（持久化重试、死信、限流）、弹幕规则调度。
- 弹幕消费：支持 `http_polling` 与 `bilibili_message_stream`（WBI + WebSocket 信息流协议）并接入统一规则执行链路。
- 数据能力：直播事件、弹幕记录、基础/高级统计（时段趋势、命中率、告警趋势）、维护任务（清理/VACUUM）。
- 开关能力：支持“简化模式 + 细粒度功能开关”（消费器/Webhook/Bot/高级统计/任务队列），按需启用高级功能。
- Provider 能力：支持 TG/钉钉/Pushoo 消息推送适配；`send_danmaku` 支持官方发送 + provider 结果通知。
- Provider 入站能力：支持 `/integration/provider/inbound/{provider}` 的签名鉴权 + 防重放 + 命令入队（自定义 HMAC + Telegram/DingTalk 官方签名可选）。
- Monitor 能力：支持真实 SMTP 测试邮件发送（SSL/STARTTLS）与运行日志查询。
- 运维能力：配置文件优先、自动生成配置、热加载、离线 Swagger UI。
- 管理员鉴权：支持 admin 登录会话 + API token 双通道鉴权；默认账号 `admin/admin`，支持登录后修改密码。

## 3. 目录结构

```text
backend/                # Go 后端
frontend/               # 前端资源（app / swagger / legacy / pages）
ffmpeg/                 # 内置 ffmpeg 二进制
main.go                 # 程序入口（embed 前端）
build.sh                # Linux/macOS 一键构建
build.bat               # Windows 一键构建
Dockerfile
docker-compose.yml
docker-update.sh        # Linux 一键更新容器
```

## 4. 本地开发运行

### 4.1 环境要求

- Go 1.25+
- Windows / Linux

### 4.2 启动

```bash
go run .
```

默认监听 `:18686`。

启动后可访问：

- 功能首页：`http://127.0.0.1:18686/`
- 管理员登录页：`http://127.0.0.1:18686/app/pages/admin-login.html`
- 1:1 扫码登录页：`http://127.0.0.1:18686/app/pages/login.html`
- 高级控制台：`http://127.0.0.1:18686/app/index.html`
- Swagger：`http://127.0.0.1:18686/swagger/`
- OpenAPI：`http://127.0.0.1:18686/openapi.json`

登录页说明：

- `app/pages/login.html` 可直接使用；
- 若以弹窗方式打开 `app/pages/login.html?autoclose=1`，扫码成功后会自动通知主页面并尝试自动关闭弹窗。
- 迁移版页面：
  - `http://127.0.0.1:18686/app/pages/cameras.html`
  - `http://127.0.0.1:18686/app/pages/gb28181.html`
  - `http://127.0.0.1:18686/app/pages/push.html`
  - `http://127.0.0.1:18686/app/pages/room.html`
  - `http://127.0.0.1:18686/app/pages/material.html`
  - `http://127.0.0.1:18686/app/pages/monitor.html`

## 5. 配置文件优先与热加载

程序启动时会自动加载配置文件；若不存在会自动创建（UTF-8 JSON）。

调试日志开关：

- `debugMode`（推荐）或 `enableDebugLogs` 设为 `true` 时，
  会将详细运行日志写入 `data/log/gover-YYYYMMDD.log`（含 ffmpeg 关键日志）。
- 两个字段会自动同步，支持热加载，无需重启。
- `autoStartPush` default is `false` (recommended). Set it to `true` if you want startup to auto-attempt push start.

默认配置路径：

- `./data/config.json`（常规）
- 若从仓库根启动并存在 `./gover`，会兼容 `./gover/data/config.json`

也可显式指定：

```bash
export GOVER_CONFIG_FILE=/path/to/config.json
```

运行时接口：

- `GET /api/v1/config`：读取当前配置
- `POST /api/v1/config`：保存并触发热加载
- `POST /api/v1/config/reload`：从磁盘重新加载

## 6. 一键构建（二进制）

### 6.1 Windows

```powershell
.\build.bat
```

### 6.2 Linux/macOS

```bash
./build.sh
```

默认输出到 `dist/`，文件名示例：

- `BilibiliLiveToolsGover_windows_amd64.exe`
- `BilibiliLiveToolsGover_linux_amd64`
- `BilibiliLiveToolsGover_linux_arm64`
- `BilibiliLiveToolsGover_linux_armv7`

可自定义前缀（例如你想改成别的名字）：

```bash
APP_NAME=MyLiveTool ./build.sh
```

```powershell
$env:APP_NAME='MyLiveTool'
.\build.bat
```

## 7. Docker 部署

### 7.1 前置步骤

先生成 Linux amd64 二进制：

```bash
./build.sh
```

确保存在：

- `dist/BilibiliLiveToolsGover_linux_amd64`
- `ffmpeg/linux-x64/ffmpeg`

### 7.2 构建与启动

```bash
docker compose up -d --build
```

默认映射：

- 端口：`18686:18686`
- 数据卷：`./data:/app/data`
- ffmpeg 卷：`./ffmpeg:/app/ffmpeg`

容器内默认环境变量：

- `GOVER_CONFIG_FILE=/app/data/config.json`
- `GOVER_FFMPEG_PATH=/app/ffmpeg/linux-x64/ffmpeg`

### 7.3 一键更新（Linux）

`docker-update.sh` 用于快速维护：

- 删除旧容器；
- 删除旧镜像；
- 重建镜像；
- 启动新容器。

```bash
./docker-update.sh
```

可选变量：

- `SERVICE_NAME`（默认 `gover`）
- `IMAGE_NAME`（默认 `gover:latest`）
- `APP_NAME`（配合 compose build args，默认 `BilibiliLiveToolsGover`）

## 8. 管理员鉴权与 API Token 兼容

### 8.1 鉴权方式（双通道）

当前后端 API 默认开启鉴权（白名单除外），支持两种访问方式：

1. 管理员登录会话（推荐用于前端）
   - 登录：`POST /api/v1/auth/login`
   - 退出：`POST /api/v1/auth/logout`
   - 状态：`GET /api/v1/auth/status`
   - 改密：`POST /api/v1/auth/password`
   - 登录成功后使用 `Authorization: Bearer <session_token>`
2. API token（兼容已有自动化脚本）
   - 优先从 `X-API-Key` 读取；
   - 若无 `X-API-Key`，也支持 `Authorization: Bearer <api_token>` 回退匹配。

`/api/v1/auth/status` 会返回 `mode` 字段：

- `admin_session`：管理员会话模式
- `api_key`：API token 模式

### 8.2 初始管理员账号

- 默认账号：`admin`
- 默认密码：`admin`

首次部署建议：

1. 先登录管理员页；
2. 立即修改密码（会使该用户所有旧会话失效并强制重新登录）。

### 8.3 API token 兼容 key 名

为兼容历史调用，后端会从 `api_key_settings` 中按以下名称匹配 token：

- `api_access_token`
- `admin_api_token`
- `api_token`
- `bilibili`（历史兼容）

注意：

- 未携带任何 token 的旧脚本在开启鉴权后会返回 401；
- 旧脚本只要改为携带上述任一已配置 token，即可继续调用。

## 9. 常用接口

- 健康检查：`GET /api/v1/health`
- 鉴权：`POST /api/v1/auth/login|logout`、`GET /api/v1/auth/status`、`POST /api/v1/auth/password`
- 推流设置：`GET/POST /api/v1/push/setting`
- 推流控制：`POST /api/v1/push/start|stop|restart`
- 推流状态：`GET /api/v1/push/status`
- 摄像头库：`GET /api/v1/cameras`、`GET /api/v1/cameras/{id}`、`POST /api/v1/cameras/save|delete`
- 摄像头一键套用推流：`POST /api/v1/cameras/{id}/apply-push`
- GB28181 配置与运行：`GET/POST /api/v1/gb28181/config`、`GET /api/v1/gb28181/status`、`POST /api/v1/gb28181/start|stop`
- GB28181 设备与目录：`GET /api/v1/gb28181/devices`、`GET /api/v1/gb28181/devices/{id}`、`POST /api/v1/gb28181/devices/save|delete`、`POST /api/v1/gb28181/devices/{id}/catalog/query`
- GB28181 会话：`POST /api/v1/gb28181/invite`、`POST /api/v1/gb28181/bye`、`GET /api/v1/gb28181/sessions`
- GB28181 会话导出/入库：
  - `GET /api/v1/gb28181/sessions/{callId}/sdp`
  - `POST /api/v1/gb28181/sessions/{callId}/camera-source`
- GB28181 会话重邀：
  - `POST /api/v1/gb28181/sessions/{callId}/reinvite`
- ONVIF 发现：`GET /api/v1/ptz/discover`
- B站错误日志：`GET /api/v1/integration/bilibili/error-logs`
- 弹幕消费器配置：`GET/POST /api/v1/integration/danmaku/consumer/setting`
- 弹幕消费器状态：`GET /api/v1/integration/danmaku/consumer/status`
- provider 入站 webhook：`POST /api/v1/integration/provider/inbound/{provider}`
- 异步任务列表/汇总：`GET /api/v1/integration/tasks`、`GET /api/v1/integration/tasks/summary`
- 异步任务死信重试：`POST /api/v1/integration/tasks/retry`
- 异步任务高级控制：
  - `POST /api/v1/integration/tasks/retry-batch`
  - `POST /api/v1/integration/tasks/cancel`
  - `POST /api/v1/integration/tasks/priority`
  - `GET/POST /api/v1/integration/tasks/queue-setting`
- 功能开关：`GET/POST /api/v1/integration/features`
- 运行时内存巡检：`GET /api/v1/integration/runtime/memory`、`POST /api/v1/integration/runtime/gc`
- 高级统计：`GET /api/v1/live/stats/advanced?hours=24&granularity=hour|day`
- 高级统计导出：`GET /api/v1/live/stats/advanced/export?hours=24&granularity=hour|day&format=csv|json&fields=...&maxRows=...`
- Monitor 测试邮件：`POST /api/v1/monitor/email/test`
- Monitor 运行日志：`GET /api/v1/monitor/status`
- 数据维护：`/api/v1/maintenance/*`

### 9.1 provider 入站签名说明（简版）

入站接口：`POST /api/v1/integration/provider/inbound/{provider}`

通用签名（Gover HMAC）：

- `X-Gover-Timestamp`: Unix 秒时间戳
- `X-Gover-Nonce`: 随机字符串
- `X-Gover-Signature`: `hex(hmac_sha256(secret, timestamp + "\n" + nonce + "\n" + body))`

`secret` 来自 API Key（`provider_inbound_secret` 或 provider 专属覆盖 key）。

官方签名（可选）：

- Telegram：校验 `X-Telegram-Bot-Api-Secret-Token`（配置 `telegram_inbound_secret_token`）。
- DingTalk：支持 `timestamp + sign`（配置 `dingtalk_inbound_sign_secret`）与 callback `msg_signature`（配置 `dingtalk_callback_token`）。

来源 IP 白名单（可选）：

- API Key：`provider_inbound_whitelist`
- 值格式：逗号分隔 `IP/CIDR`，例如 `127.0.0.1,10.0.0.0/8,192.168.1.0/24`

### 9.2 HTTP 轮询消费器字段映射（configJson）

`provider=http_polling` 时，`configJson` 支持按真实接口差异配置：

- 自定义请求方法：`method=GET|POST`
- 自定义鉴权注入：`auth.mode=bearer|header|query|body`
- 自定义分页游标语义：`paging.cursorMode=cursor|offset|page`
- 自定义字段映射：`mapping.itemsPath/contentPath/uidPath/...`

示例：

```json
{
  "method": "POST",
  "headers": { "X-Client": "gover" },
  "auth": { "mode": "header", "name": "X-Token" },
  "paging": {
    "cursorField": "offset",
    "cursorIn": "body",
    "cursorMode": "offset",
    "responseCursorPath": "data.next_offset",
    "limitField": "page_size",
    "roomIdField": "room_id"
  },
  "mapping": {
    "itemsPath": "data.records",
    "roomIdPath": "room_id",
    "uidPath": "user.uid",
    "unamePath": "user.name",
    "contentPath": "message",
    "rawPayloadPath": "raw"
  }
}
```

## 10. 注意事项

- SQLite 已开启外键及并发优化参数；清理后可通过 VACUUM 压缩数据库体积。
- 长时间运行时可配合维护清理任务与 `runtime/memory` 接口观察内存/任务堆积趋势。
- 若使用 Pushoo 作为消息推送通道，可在 API Key 中配置：
  - `pushoo`：例如 `http://127.0.0.1:8084/send`
  - `pushoo_token`：可选，若 Pushoo 开启 token 校验时使用
- 若启用 provider 入站 webhook，可配置：
  - `provider_inbound_secret`（通用密钥）
  - `telegram_inbound_secret` / `dingtalk_inbound_secret`（Gover HMAC 的 provider 覆盖）
  - `telegram_inbound_secret_token`（Telegram 官方 secret token）
  - `dingtalk_inbound_sign_secret`（DingTalk `timestamp+sign`）
  - `dingtalk_callback_token`（DingTalk callback `msg_signature`）
  - `provider_inbound_whitelist`（可选来源 IP 白名单）
  - `provider_inbound_skew_sec`（时间戳允许偏移秒数，默认 300）
- Bilibili API 可能变更，建议定期查看错误日志与告警统计。
- 若 IDE 出现 Go 红标，优先检查是否打开了仓库根并识别 `go.work`。
