# QQ Bot 渠道

clawdex QQ Bot 渠道的详细参考文档。

## 概述

QQ Bot 渠道通过 QQ 官方机器人开放平台 API 接入 QQ。驱动维护一条持久的
WebSocket 连接接收消息，并通过 REST API 发送回复和媒体。

主要特点：

- **官方 API** — 基于 QQ 机器人开发者平台（q.qq.com）
- **WebSocket 传输** — 带心跳、自动重连和会话恢复
- **私聊 + 群聊** — 同时支持 C2C 私信和群内 @机器人对话
- **自动重连** — 断线后指数退避重连，支持 Resume 恢复
- **媒体支持** — 图片、音频、视频、文件通过富媒体 API 收发
- **被动回复限流** — 超过 4 条自动切换为主动消息模式

## 设置

1. 在 [QQ 开放平台](https://q.qq.com) 创建机器人应用。
2. 在开发者控制台获取 **App ID** 和 **Client Secret**。
3. 开启机器人消息意图（C2C 消息、群 @ 消息）。
4. 运行 `clawdex onboard`，选择 **Add QQ Bot instance**。

```
$ clawdex onboard
...
  Choice [1/2/3/4/5/6]: 4

QQ Bot
  Instance name [qqbot]:

  Get your AppID and ClientSecret from https://q.qq.com

  App ID: 123456789
  Client Secret: ********************************

  ✓ QQ Bot "qqbot" configured
  ℹ DM policy set to open, group policy set to open
```

完成后配置文件内容：

```json
{
  "channels": {
    "qqbot": {
      "type": "qqbot",
      "enabled": true,
      "app_id": "${QQ_APP_ID}",
      "client_secret": "${QQ_CLIENT_SECRET}",
      "dm_policy": "open",
      "allow_from": [],
      "group_policy": "open",
      "group_allow_from": []
    }
  }
}
```

## 权限控制

### 私聊策略

通过 `dm_policy` 字段配置。

| 策略 | 行为 |
|------|------|
| `open`（默认） | 响应所有私聊消息 |
| `pairing` | 未知用户收到配对码，管理员通过 CLI 审批 |
| `allowlist` | 仅 `allow_from` 中的用户 openid 可交互 |

### 群聊策略

通过 `group_policy` 字段配置。

| 策略 | 行为 |
|------|------|
| `open` | 响应所有已加入群的 @提及 |
| `allowlist`（默认） | 仅 `group_allow_from` 中列出的群生效 |
| `disabled` | 忽略所有群消息 |

群消息需要 @机器人才会触发（`GROUP_AT_MESSAGE_CREATE`）。
`@机器人名` 前缀会在传给 Codex 之前自动去除。

## 配置参考

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `type` | string | — | 必须为 `"qqbot"` |
| `enabled` | bool | `true` | 启用/禁用此实例 |
| `app_id` | string | — | 机器人 App ID（支持 `${ENV_VAR}`） |
| `client_secret` | string | — | Client Secret（支持 `${ENV_VAR}`） |
| `dm_policy` | string | `"open"` | 私聊权限：`open`、`pairing`、`allowlist` |
| `allow_from` | string[] | `[]` | 用户 openid 白名单 |
| `group_policy` | string | `"allowlist"` | 群聊权限：`open`、`allowlist`、`disabled` |
| `group_allow_from` | string[] | `[]` | 群 openid 白名单 |
| `text_chunk_limit` | int | `5000` | 单条消息最大字符数 |

## 环境变量

凭证可通过环境变量提供，避免明文写入配置：

```bash
export QQ_APP_ID="123456789"
export QQ_CLIENT_SECRET="your-client-secret-here"
```

在配置中以 `${QQ_APP_ID}` 和 `${QQ_CLIENT_SECRET}` 引用。

## 协议细节

### 连接生命周期

1. 通过 `POST https://bots.qq.com/app/getAppAccessToken` 获取 Access Token
2. 通过 `GET https://api.sgroup.qq.com/gateway` 获取 WebSocket 网关地址
3. 建立 WebSocket 连接，收到 Hello（op:10）及心跳间隔
4. 发送 Identify（op:2）携带 token 和 intents；或 Resume（op:6）恢复已有会话
5. 收到 READY 事件，获得 session ID
6. 进入事件循环：心跳 + 事件分发

### 自动重连

断线后按指数退避重试（3 秒 → 60 秒封顶）。
若服务端返回 Invalid Session（op:9），会清除会话状态，下次连接时重新 Identify。

### 被动回复限流

QQ Bot 平台限制每条入站消息最多被动回复 4 次/小时。驱动按触发消息跟踪回复计数，
达到限额后自动去掉 `msg_id`（切换为主动消息模式）。对 Codex 透明无感。

### Token 刷新

Access Token 会被缓存，在过期前 60 秒自动刷新。若请求返回 401，驱动清除缓存后
自动重试一次。

## 媒体

### 入站（用户 → 机器人）

| 类型 | 处理方式 |
|------|----------|
| 文本 | 直接传给 Codex |
| 图片 | 下载到临时文件，作为 `--image` 参数传入 |
| 音频 | 下载到临时文件 |
| 视频 | 下载到临时文件 |
| 文件 | 下载到临时文件 |

附件在 Codex 会话结束后自动清理。

### 出站（机器人 → 用户）

Codex 输出中包含文件路径时，网关通过 QQ 富媒体 API 上传
（`/v2/users/{id}/files` 或 `/v2/groups/{id}/files`）：

- 图片（`.jpg`、`.png`、`.gif`、`.webp`）— file_type 1
- 视频（`.mp4`、`.mov`、`.avi`）— file_type 2
- 音频（`.mp3`、`.wav`、`.silk`）— file_type 3
- 其他文件 — file_type 4

## 机器人指令

所有 clawdex 标准指令均可使用：

| 指令 | 说明 |
|------|------|
| `/help` | 显示可用指令 |
| `/new` | 开始新对话 |
| `/sessions` | 列出最近会话 |
| `/resume <id>` | 切换到已有会话 |
| `/cancel` | 取消当前运行中的任务 |
| `/status` | 显示当前聊天上下文 |

## SOUL.md

支持按实例设置 SOUL 提示词。创建
`~/.clawdex/SOUL-<实例名>.md` 为特定 QQ Bot 设定提示：

```bash
cat > ~/.clawdex/SOUL-qqbot.md << 'EOF'
你是一个友好的编程助手。
EOF
```

若实例专属文件不存在，则回退到全局 `~/.clawdex/SOUL.md`。

## 常见问题

### "灵魂不在线"（机器人显示离线）

机器人离线说明没有活跃的 WebSocket 连接。

- 确认网关正在运行：`clawdex gateway status`
- 检查日志：`clawdex gateway run`（前台模式）
- 确认 App ID 和 Client Secret 正确
- 在 q.qq.com 确认消息意图已开启

### 收不到消息

- 检查 `group_policy` — 若为 `allowlist`，群 openid 必须在 `group_allow_from` 中
- 群里必须 @机器人才会触发
- 检查 `dm_policy` — 若为 `allowlist`，发送者 openid 必须在 `allow_from` 中

### 日志中出现 Token 错误

- 确认 `client_secret` 正确且未过期
- 驱动遇到 401 会自动重试；持续失败说明凭证无效
- 如需要，在 q.qq.com 重新生成密钥

### 启动时崩溃

- 确保 `app_id` 和 `client_secret` 非空
- 若使用 `${ENV_VAR}` 语法，确认环境变量已 export
