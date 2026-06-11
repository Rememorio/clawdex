# clawdex

[English](README.md)

Go 语言实现的网关服务，将多渠道消息转发到原生 Codex CLI 并返回结果。

## 功能特性

- **多渠道支持** — Telegram、企业微信（WeCom）、微信（Weixin）、QQ Bot 和飞书统一消息处理
- **原生 Codex 集成** — 直接 CLI 桥接，支持会话持久化
- **流式回复** — 实时打字机效果（Telegram 部分编辑、企微 WebSocket 流式消息）
- **访问控制** — 配对码验证、白名单、群组权限管理
- **媒体支持** — 图片、文件、语音消息（因渠道而异）
- **会话管理** — 恢复对话、切换上下文、重启后保持会话
- **守护进程模式** — 后台运行，支持 systemd 集成
- **企微多实例** — 单个网关运行多个企微机器人

## 快速开始

### 前置条件

安装 clawdex 前，请确保已准备：

1. **Go 1.24+** — 从源码构建时需要
2. **Codex CLI** — 安装并配置 [OpenAI Codex](https://github.com/openai/codex)
   ```bash
   # 验证 Codex 是否正常工作
   codex "2+2等于几？"
   ```
3. **渠道凭证** — 选择一个或多个：
   - **Telegram**：从 [@BotFather](https://t.me/BotFather) 获取机器人令牌
   - **企业微信**：Token + EncodingAESKey（webhook）或 BotID + Secret（websocket）
   - **微信**：无需预设置 — onboard 时扫码即可
   - **QQ Bot**：从 [q.qq.com](https://q.qq.com) 获取 App ID + Client Secret
   - **飞书**：从 [飞书开放平台](https://open.feishu.cn) 获取 App ID + App Secret

### 环境变量

推荐将凭证设置为环境变量：

```bash
# Telegram（如使用 Telegram）
export TELEGRAM_BOT_TOKEN="123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

# 企业微信 Webhook 模式（如使用 webhook）
export WECOM_TOKEN="your-callback-token"
export WECOM_ENCODING_AES_KEY="your-43-character-encoding-aes-key"
export WECOM_WEBHOOK_PATH="/wecom/webhook"

# 企业微信 WebSocket 模式（如使用 websocket）
export WECOM_BOTID="your-bot-id"
export WECOM_SECRET="your-websocket-secret"

# QQ Bot（如使用 QQ）
export QQ_APP_ID="your-app-id"
export QQ_CLIENT_SECRET="your-client-secret"

# 飞书（如使用飞书）
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your-app-secret"
```

也可以添加到 `~/.clawdex/env`（systemd 服务会自动加载）：

```bash
cat > ~/.clawdex/env << 'EOF'
TELEGRAM_BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
WECOM_BOTID=your-bot-id
WECOM_SECRET=your-websocket-secret
EOF
```

### 安装

```bash
# 安装（需要 Go 1.24+）
go install github.com/Rememorio/clawdex/cmd/clawdex@latest

# 从最新 GitHub Release 更新已安装的可执行文件
clawdex update

# 交互式配置 — 配置 Codex、Telegram 和/或企业微信
clawdex onboard

# 或一步完成配置和安装守护进程（Linux）
clawdex onboard --install-daemon

# 启动网关
clawdex gateway start
```

就这么简单。打开你的 Telegram 机器人（或企微群）发送消息即可。

### 快速配置

```bash
clawdex config list                         # 显示所有配置值
clawdex config get <KEY>                    # 获取配置值
clawdex config set <KEY> <VALUE>            # 设置配置值
clawdex config file                         # 显示配置文件路径
```

## 守护进程管理

```bash
clawdex daemon install      # 安装 systemd 用户服务（Linux）
clawdex daemon uninstall    # 移除 systemd 用户服务
clawdex update              # 更新当前可执行文件
clawdex gateway start       # 启动后台守护进程
clawdex gateway run         # 前台运行（调试用）
clawdex gateway status      # 查看进程状态
clawdex gateway stop        # 停止守护进程
clawdex gateway restart     # 重启守护进程
```

### systemd 用户服务（Linux）

`daemon install` 将 clawdex 注册为 systemd 用户服务，支持自动重启和开机自启——无需 root 权限：

```bash
clawdex gateway install
```

这将创建 `~/.config/systemd/user/clawdex-gateway.service`，启用并立即启动服务。服务失败时自动重启（`RestartSec=5`），通过 `loginctl enable-linger` 实现登出后继续运行。环境变量可在 `~/.clawdex/env` 中设置。

查看日志：

```bash
journalctl --user -u clawdex-gateway -f
```

移除服务：

```bash
clawdex gateway uninstall
```

## 配置

配置存储在 `~/.clawdex/clawdex.json`。所有设置也可通过环境变量覆盖。运行 `clawdex onboard` 进行交互式配置。

### 配置文件结构

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "bot_token": "${TELEGRAM_BOT_TOKEN}",
      "dm_policy": "pairing",
      "streaming": "partial"
    },
    "wecom-primary": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    },
    "feishu": {
      "type": "feishu",
      "enabled": true,
      "app_id": "${FEISHU_APP_ID}",
      "app_secret": "${FEISHU_APP_SECRET}",
      "dm_policy": "pairing",
      "group_policy": "allowlist",
      "require_mention": true
    }
  },
  "codex": {
    "workdir": "/home/user/project",
    "sandbox": "workspace-write",
    "timeout": "120m"
  },
  "gateway": {
    "address": ":8080"
  },
  "cron": {
    "enabled": true,
    "store": "cron/jobs.json",
    "mcp_enabled": true
  }
}
```

如果没有配置 `codex.workdir`，clawdex 会自动创建并使用
`~/.clawdex/workspace`。

### 通配符配置

使用 `channels.*.<字段>` 为所有渠道设置某个字段：

```bash
clawdex config set 'channels.*.allow_from' 'YOUR_USER_ID'
```

## 用户审批（配对）

默认情况下，未知用户会收到配对码。通过 CLI 审批：

```bash
clawdex pairing list              # 列出待审批请求
clawdex pairing approve <CODE>    # 审批用户
```

其他访问控制模式参见 [docs/TELEGRAM_CN.md](docs/TELEGRAM_CN.md)。

## 机器人命令

Telegram、企业微信、微信、QQ Bot 和飞书均支持：

| 命令 | 说明 |
|------|------|
| `/help` | 显示可用命令 |
| `/new` | 开始新对话 |
| `/sessions` | 列出最近会话（最多 10 个） |
| `/resume <id>` | 切换到已有会话 |
| `/cancel` | 取消当前运行中的任务 |
| `/status` | 显示当前聊天上下文：渠道、作用域、会话、SOUL.md |
| `/cron help` | 显示定时任务命令 |
| `/cron list` | 列出当前聊天的定时任务 |
| `/cron status <id\|序号\|名称>` | 查看定时任务详情 |
| `/cron stop <id\|序号\|名称>` | 暂停定时任务 |
| `/cron resume <id\|序号\|名称>` | 恢复定时任务 |
| `/cron remove <id\|序号\|名称>` | 删除定时任务 |
| `/cron clear` | 删除当前聊天的所有定时任务 |

## 定时任务

clawdex 支持用自然语言创建提醒和周期任务。当聊天请求里包含明确的时间、间隔、周期或 cron 表达式时，Codex 可以调用内置的 `clawdex_cron` MCP tool，为当前聊天创建任务。任务默认持久化到 `~/.clawdex/cron/jobs.json`。

调度器支持 RFC3339 一次性时间、固定间隔，以及带可选 IANA 时区的五段 cron 表达式。固定提醒会发送保存的文本；agent 任务会在调度时间重新运行 Codex，并把新的结果发回原聊天。

运行时配置：

| 配置项 | 环境变量 | 默认值 | 说明 |
|--------|----------|--------|------|
| `cron.enabled` | `CRON_ENABLED` | `true` | 启用调度器 |
| `cron.store` | `CRON_STORE` | `cron/jobs.json` | 任务存储路径；相对路径基于 `~/.clawdex` |
| `cron.mcp_enabled` | `CRON_MCP_ENABLED` | `true` | 向 Codex 暴露 cron MCP tool |

`clawdex mcp-server cron` 是 Codex 通过网关上下文令牌调用的 stdio MCP 入口，通常不需要手动运行。

## SOUL.md

创建 `~/.clawdex/SOUL.md` 可向每次 Codex 会话注入系统提示词：

```bash
cat > ~/.clawdex/SOUL.md << 'EOF'
你是一个有帮助的编程助手。
EOF
```

SOUL 文件会在新的 Codex 会话开始时重新读取。修改 SOUL 文件后，在聊天中使用
`/new`，下一条消息就会应用新内容。

多实例企微配置可使用 `~/.clawdex/SOUL-<name>.md` 实现实例级提示词。

## 诊断

```bash
clawdex doctor            # 检查配置健康状态
clawdex doctor --fix      # 检查并自动修复问题
```

## 文档

- [Telegram 渠道](docs/TELEGRAM_CN.md) — 配置参考、访问控制、流式输出、媒体、命令。
- [企业微信渠道](docs/WECOM_CN.md) — 企业微信配置、加密、webhook 配置、多实例。
- [微信渠道](docs/WEIXIN_CN.md) — 微信个人号设置、扫码登录、输入提示、媒体。
- [QQ Bot 渠道](docs/QQBOT_CN.md) — QQ Bot 设置、WebSocket 网关、群@提及、媒体上传。
- [飞书渠道](docs/FEISHU_CN.md) — 飞书长连接、接收消息事件、访问控制。
- [定时任务](docs/CRON_CN.md) — 自然语言提醒、周期任务和 cron 配置。

## 架构

```
cmd/clawdex          CLI 入口
internal/
  app/               CLI 命令处理（gateway, config, pairing）
  channel/           渠道抽象（Driver, Responder, Message）
  channel/telegram/  Telegram 长轮询驱动
  channel/wecom/     企业微信 webhook & WebSocket 驱动
  channel/weixin/    微信个人号长轮询驱动
  channel/qqbot/     QQ Bot WebSocket 驱动
  channel/feishu/    飞书长连接驱动
  gateway/           消息编排、工作池、斜杠命令
  codex/             原生 Codex CLI 桥接（codex exec）
  cron/              持久化定时任务
  mcp/               暴露给 Codex 的本地 MCP 工具
  config/            配置加载（文件 + 环境变量）
  onboard/           交互式配置向导
  server/            HTTP 服务器（/healthz）
  daemon/            PID 文件进程生命周期 + systemd 安装
  doctor/            配置健康检查
  pairing/           配对码存储
  secret/            密钥引用解析（${ENV}, file://）
  logger/            基于 slog 的结构化日志
```

## 开发

```bash
# 运行测试
go test ./...

# 构建
go build ./cmd/clawdex

# 前台运行（带调试日志）
clawdex gateway run
```

## 许可证

MIT License。参见 [LICENSE](LICENSE)。
