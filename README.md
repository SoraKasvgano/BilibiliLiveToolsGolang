# BilibiliLiveToolsGover

Go rewrite of BilibiliLiveTools (backend + frontend), focused on Bilibili live streaming orchestration with FFmpeg.

## 1. 项目简介

`BilibiliLiveToolsGover` 是对原 C# 工具[BilibiliLiveTools](https://github.com/withsalt/BilibiliLiveTools)的 Go 重写版本，目标是：

- 尽量保持原功能 1:1 行为兼容；
- 扩展 RTSP / MJPEG / ONVIF PTZ 场景；
- 支持多路拼屏推流（拖拽排布、主画面优先、标题叠加）；
- 支持弹幕控制 PTZ、机器人命令、Webhook 编排能力；
- 使用纯 Go 依赖（SQLite 使用 `modernc.org/sqlite`，避免 cgo 依赖）；
- 前端资源使用 `embed` 自动嵌入到二进制。

## 2. 当前主要能力

- 推流输入：视频、USB 摄像头、RTSP、MJPEG、桌面、ONVIF（PTZ联动）。
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

## 3. 目录结构

```text
gover/
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
cd gover
go run .
```

默认监听 `:18686`。

启动后可访问：

- 控制台：`http://127.0.0.1:18686/`
- Swagger：`http://127.0.0.1:18686/swagger/`
- OpenAPI：`http://127.0.0.1:18686/openapi.json`
- 迁移版页面：
  - `http://127.0.0.1:18686/app/pages/push.html`
  - `http://127.0.0.1:18686/app/pages/room.html`
  - `http://127.0.0.1:18686/app/pages/material.html`
  - `http://127.0.0.1:18686/app/pages/monitor.html`

## 5. 配置文件优先与热加载

程序启动时会自动加载配置文件；若不存在会自动创建（UTF-8 JSON）。

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
cd gover
.\build.bat
```

### 6.2 Linux/macOS

```bash
cd gover
./build.sh
```

默认输出到 `gover/dist`，文件名示例：

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
cd gover
./build.sh
```

确保存在：

- `dist/BilibiliLiveToolsGover_linux_amd64`
- `ffmpeg/linux-x64/ffmpeg`

### 7.2 构建与启动

```bash
cd gover
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
cd gover
./docker-update.sh
```

可选变量：

- `SERVICE_NAME`（默认 `gover`）
- `IMAGE_NAME`（默认 `gover:latest`）
- `APP_NAME`（配合 compose build args，默认 `BilibiliLiveToolsGover`）

## 8. 常用接口

- 健康检查：`GET /api/v1/health`
- 推流设置：`GET/POST /api/v1/push/setting`
- 推流控制：`POST /api/v1/push/start|stop|restart`
- 推流状态：`GET /api/v1/push/status`
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

### 8.1 provider 入站签名说明（简版）

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

### 8.2 HTTP 轮询消费器字段映射（configJson）

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

## 9. 注意事项

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
