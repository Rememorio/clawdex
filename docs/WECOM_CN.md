# 企业微信（WeCom）渠道

clawdex 企业微信渠道的详细参考文档。

## 概述

企业微信支持两种连接模式：

- **Webhook 模式**（默认）— 企业微信将加密消息推送到 HTTP 端点。支持**消息通知机器人**（XML 格式）和**智能机器人**（JSON 格式）。需要 `token` 和 `encoding_aes_key`。
- **WebSocket 模式** — 通过持久 WebSocket 连接（`wss://openws.work.weixin.qq.com`）连接企业微信。只需 `botid` 和 `secret`。支持**流式回复**（打字机效果）。

## 机器人类型

企业微信有两种群机器人类型：

| | 消息通知机器人 | 智能机器人 |
|---|---|---|
| 连接模式 | 仅 Webhook | Webhook 或 WebSocket |
| 消息加密信封 | XML | JSON |
| 解密后消息格式 | XML | JSON |
| 回复机制 | 固定 `webhook_url`（可复用） | 一次性 `response_url`（单次使用，1小时过期） |
| 流式支持 | 否 | 是（仅 WebSocket 模式） |
| 配置凭证 | Token + EncodingAESKey | Token + EncodingAESKey（webhook）或 BotID + Secret（websocket） |

两种机器人类型在 webhook 模式下均受支持——驱动会自动检测信封格式（XML vs JSON）并相应解析。

## 机器人设置

### Webhook 模式（默认）

1. 在企业微信中创建机器人——**消息通知机器人**或**智能机器人**——并配置回调 URL。
2. 从机器人管理页面复制：
   - **Token** — 回调验证令牌
   - **EncodingAESKey** — 43 字符 AES 加密密钥
3. 将配置添加到 `~/.clawdex/clawdex.json`：

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "enabled": true,
      "token": "your-callback-verification-token",
      "encoding_aes_key": "your-43-character-encoding-aes-key",
      "webhook_path": "/wecom/webhook"
    }
  }
}
```

4. 将机器人回调 URL 设为 `https://your-server:8080/wecom/webhook`。

`clawdex onboard` 创建的第一个企业微信实例默认名为 `wecom`。如果你保留
默认名，可直接使用以下 CLI：

```bash
clawdex config set channels.wecom.enabled true
clawdex config set channels.wecom.token '${WECOM_TOKEN}'
clawdex config set channels.wecom.encoding_aes_key '${WECOM_ENCODING_AES_KEY}'
clawdex config set channels.wecom.webhook_path /wecom/webhook
```

### WebSocket 模式

1. 在企业微信管理后台创建智能机器人。
2. 从机器人管理页面复制：
   - **BotID** — 机器人唯一标识。
   - **Secret** — WebSocket 连接密钥。
3. 将配置添加到 `~/.clawdex/clawdex.json`：

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

无需 HTTP 回调 URL——机器人向企业微信 WebSocket 服务器发起出站连接。

或使用非交互式 CLI：

```bash
clawdex config set channels.wecom.enabled true
clawdex config set channels.wecom.connection_mode websocket
clawdex config set channels.wecom.botid your-bot-id
clawdex config set channels.wecom.secret '${WECOM_SECRET}'
```

### Token 和 Secret 格式

Token、encoding_aes_key 和 secret 值支持相同格式：

| 格式 | 示例 | 说明 |
|------|------|------|
| 明文字符串 | `abc123...` | 直接存储 |
| 环境变量引用 | `${WECOM_TOKEN}` | 运行时从环境变量解析 |
| 文件引用 | `file:///run/secrets/wecom_token` | 运行时从文件读取 |

### 推荐：环境变量

`clawdex onboard` 向导默认使用环境变量引用。启动网关前设置：

**Webhook 模式：**
```bash
export WECOM_TOKEN="your-callback-verification-token"
export WECOM_ENCODING_AES_KEY="your-43-character-encoding-aes-key"
```

**WebSocket 模式：**
```bash
export WECOM_SECRET="your-websocket-secret"
```

配置文件中将包含 `${WECOM_TOKEN}`、`${WECOM_ENCODING_AES_KEY}` 或 `${WECOM_SECRET}`，运行时解析。这样可以避免在磁盘上明文存储密钥。

## 连接模式

### Webhook

默认模式。企业微信将加密消息推送到 HTTP 端点。网关自动检测信封格式：

- **消息通知机器人**：XML 信封 → XML 消息体，通过可复用的 `webhook_url` 回复
- **智能机器人**：JSON 信封 → JSON 消息体，通过一次性的 `response_url` 回复

要求：
- `token`、`encoding_aes_key`、`webhook_path`
- 在网关服务器上注册 HTTP 路由
- webhook 模式不支持流式输出（两种机器人类型都不支持通过 HTTP 编辑消息）

### WebSocket

机器人连接到 `wss://openws.work.weixin.qq.com` 并维持持久连接。消息通过 `aibot_msg_callback` 命令以 JSON 帧形式到达。回复以 `aibot_respond_msg` 帧发送。

- 需要：`botid`、`secret`
- 无需 HTTP 回调 URL——不注册入站 HTTP 路由
- 支持**流式回复**（通过 `msgtype: "stream"`）
- 连接断开时自动重连（3 秒延迟）
- 每 30 秒发送心跳 ping（可通过 `heartbeat_interval` 配置）

#### WebSocket 协议

WebSocket 连接使用 JSON 帧协议：

```
→ aibot_subscribe   {bot_id, secret}      # 连接时订阅
→ ping              {}                      # 心跳（每 30 秒）
← aibot_msg_callback    {message JSON}     # 入站用户消息
← aibot_event_callback  {event JSON}       # 入站事件（忽略）
→ aibot_respond_msg     {msgtype, markdown/stream}  # 回复
→ aibot_send_msg        {chatid, msgtype, markdown}  # 主动发送
```

每帧有一个 `cmd` 字段和 `headers.req_id`。入站回调的 `req_id` **必须**在回复帧中回传。
主动发送会生成新的本地 `req_id` 并使用 `aibot_send_msg`，不依赖缓存的回调
`req_id`。

## 访问控制

通过 `dm_policy`（配置文件）或 `WECOM_DM_POLICY`（环境变量）控制。

| 策略 | 行为 |
|------|------|
| `pairing`（默认） | 未知用户收到配对码，管理员通过 CLI 审批 |
| `open` | 所有用户可与机器人交互 |
| `allowlist` | 只有 `allow_from` 列表中的 UserID 可以交互 |

### 配对流程

1. 用户向机器人发送消息。
2. 机器人回复一个 6 位配对码（例如 `A3X9K2`）。
3. 管理员运行：
   ```bash
   clawdex pairing approve A3X9K2
   ```
4. UserID 被添加到运行时白名单，**同时**持久化到配置文件的
   `allow_from` 中。变更即时生效，无需重启网关。

### 白名单

将 `dm_policy` 设为 `"allowlist"` 并列出 UserID：

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "dm_policy": "allowlist",
      "allow_from": ["user1", "user2"]
    }
  }
}
```

如果你使用 `clawdex config`，常见命令如下：

```bash
clawdex config set channels.wecom.dm_policy allowlist
clawdex config set channels.wecom.allow_from user1,user2
```

## 获取 ChatID 和 UserID

企业微信管理后台不显示 ChatID 或 UserID。最简单的方法是从网关日志获取：

1. 临时将 `group_policy` 设为 `"open"` 以接受所有群消息：
   ```bash
   clawdex config set channels.wecom.group_policy open
   clawdex gateway restart
   ```

2. 在目标群发送消息。

3. 查看网关日志——每条入站消息会打印：
   ```
   wecom recv: type=text from=zhangsan chat=wrkSFfCgAAxxxxxx
   ```
   WebSocket 模式下对应日志为：
   ```
   wecom websocket recv: type=text from=zhangsan chat=wrkSFfCgAAxxxxxx req_id=...
   ```
   - `from=` 是 **UserID**，用于 `allow_from` 白名单。
   - `chat=` 是 **ChatID**，用于 `groups` 映射键或
     `group_allow_from` 列表。

4. 将值复制到配置中，然后切回 `"allowlist"`：
   ```json
   {
     "channels": {
       "wecom": {
         "type": "wecom",
         "group_policy": "allowlist",
         "groups": {
           "wrkSFfCgAAxxxxxx": {
             "allow_from": ["zhangsan"]
           }
         }
       }
     }
   }
   ```

查看日志：

```bash
# 前台模式
clawdex gateway run

# systemd 服务
journalctl --user -u clawdex-gateway -f

# 后台守护进程
tail -f ~/.clawdex/gateway.log
```

> **提示：** 你也可以用 `"*": {}` 作为临时通配符接受所有群，收集 ID
> 后再替换为具体条目。

## 群组访问控制

群消息通过 `group_policy`（配置文件）或 `WECOM_GROUP_POLICY`
（环境变量）控制。

| 策略 | 行为 |
|------|------|
| `allowlist`（默认） | 只有 `group_allow_from` 或 `groups` 映射中列出的群被允许；`"*"` 作为通配符回退 |
| `open` | 所有群可与机器人交互 |
| `disabled` | 丢弃所有群消息 |

### Groups 映射

`groups` 字段是从 ChatID 到规则对象的映射。每个规则可选择性指定：

- **`enabled`**：`true`（默认）或 `false`，用于禁用特定群。
- **`allow_from`**：群内发送者白名单；为空表示所有群成员都可使用机器人。

特殊键 `"*"` 作为未明确列出的任何群的通配符回退。

#### 访问检查流程

```
收到群消息
  → group_policy == "disabled"   → 丢弃
  → group_policy == "open"       → 允许
  → group_policy == "allowlist"  →
      如果 group_allow_from 非空：
        chatID 必须在 group_allow_from（或 "*"）
          → 未找到 → 丢弃
      查找 groups[chatID]，回退 groups["*"]
        → 未找到条目：
            如果 group_allow_from 匹配 → 允许
            否则 → 丢弃
        → entry.enabled == false → 丢弃
        → entry.allow_from 非空 && 发送者不在 allow_from → 丢弃
        → 允许（如有 @提及前缀则去除后继续派发）
```

### 示例

**允许特定群，限制其中一群的用户：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "allowlist",
      "groups": {
        "wrkSFfCgAAxxxxxx": {
          "allow_from": ["zhangsan", "lisi"]
        },
        "wrkSFfCgAAyyyyyy": {}
      }
    }
  }
}
```

- `wrkSFfCgAAxxxxxx`：只有 `zhangsan` 和 `lisi` 可以触发机器人。
- `wrkSFfCgAAyyyyyy`：所有成员都可以触发机器人。
- 其他任何群：丢弃。

**使用 `group_allow_from` 简单群白名单：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "allowlist",
      "group_allow_from": ["wrkSFfCgAAxxxxxx", "wrkSFfCgAAyyyyyy"]
    }
  }
}
```

- 只有列出的两个群被允许；不做群内发送者过滤。

**结合 `group_allow_from` 和群规则：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "allowlist",
      "group_allow_from": ["wrkSFfCgAAxxxxxx", "wrkSFfCgAAyyyyyy"],
      "groups": {
        "wrkSFfCgAAxxxxxx": {
          "allow_from": ["zhangsan"]
        }
      }
    }
  }
}
```

- `wrkSFfCgAAxxxxxx`：`group_allow_from` 允许，但只有 `zhangsan`
  可触发。
- `wrkSFfCgAAyyyyyy`：`group_allow_from` 允许，所有成员都可触发。
- 其他群：丢弃。

**默认允许所有群，禁用一个：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "allowlist",
      "groups": {
        "wrkSFfCgAAzzzzzz": { "enabled": false },
        "*": {}
      }
    }
  }
}
```

- `wrkSFfCgAAzzzzzz`：明确禁用。
- 其他所有群：通过 `"*"` 通配符允许。

**带发送者过滤的通配符（任何群只有管理员）：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "allowlist",
      "groups": {
        "*": { "allow_from": ["admin1", "admin2"] }
      }
    }
  }
}
```

**对所有群开放（无需群配置）：**

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "group_policy": "open"
    }
  }
}
```

或通过 CLI：

```bash
clawdex config set channels.wecom.group_policy open
```

## 按聊天类型设置沙箱

默认情况下，Codex 沙箱级别（`codex.sandbox`）适用于所有消息。你可以使用 `codex.group_sandbox` 为群消息设置更严格的沙箱：

```json
{
  "codex": {
    "sandbox": "danger-full-access",
    "group_sandbox": "read-only"
  }
}
```

- **私聊消息**使用 `codex.sandbox`（如 `danger-full-access`）
- **群消息**使用 `codex.group_sandbox`（如已设置），否则回退到 `codex.sandbox`

这允许为受信任的私聊用户运行宽松沙箱，同时将群聊限制为只读访问。

通过环境变量设置：

```bash
export CODEX_GROUP_SANDBOX=read-only
```

> **注意：** `groups` 映射仅从配置文件加载。由于其嵌套结构，不支持环境变量。

## 消息类型

### 入站（用户 -> 机器人）

**Webhook 模式**（加密 XML）：

| MsgType | 处理方式 |
|---------|----------|
| `text` | 文本内容转发给 Codex |
| `image` | 图片下载后作为 `--image` 传递给 Codex |
| `mixed` | 图片下载，从文章提取文本，转发给 Codex |
| `link` | 提取标题、描述和 URL 作为文本转发 |
| `voice` | 发送为 `[voice]` 占位符（企微回调不推送） |
| `file` | 发送为 `[file: name]` 占位符（企微回调不推送） |
| `location` | 发送为 `[location]` 占位符 |
| `event` | 忽略 |

> **注意：** 企业微信群机器人回调只推送 `text`、`image` 和 `mixed` 消息。其他类型（语音、视频、文件、位置）不会被企业微信推送。

**WebSocket 模式**（JSON）：

| MsgType | 处理方式 |
|---------|----------|
| `text` | 文本内容转发给 Codex |
| `image` | 图片 URL 下载后作为 `--image` 传递给 Codex |
| `voice` | 如有转录文本则使用（智能机器人），否则 `[voice]` |
| `file` | `[file]` 占位符 |
| `mixed` | 从 `msg_item` 数组提取文本和图片 |
| `event` | 忽略 |

### 出站（机器人 -> 用户）

**Webhook 模式：** Codex 输出通过 webhook URL 作为 **markdown** 消息发送。在输出中检测到文件路径时：

- **图片**（`.jpg`、`.jpeg`、`.png`）：作为 base64 编码图片消息发送（最大 2MB；更大文件回退到文件上传）
- **其他文件**：通过 `upload_media` API 上传，然后作为文件消息发送（最大 20MB）

**WebSocket 模式：** Codex 输出通过 `aibot_respond_msg` 发送。文本使用 `markdown` 或 `stream` 帧；检测到本地文件路径时，clawdex 会通过 `aibot_upload_media_init`、`aibot_upload_media_chunk` 和 `aibot_upload_media_finish` 上传工作区产物，再以 `file`、`image`、`voice` 或 `video` 消息回传。当前限制遵循官方 AI Bot 长连接规则：图片最大 2 MB，语音最大 2 MB（`.amr`），视频最大 10 MB（`.mp4`），普通文件最大 20 MB。

## 消息分块

企业微信 markdown 消息限制为 **4096 UTF-8 字节**。长消息在自然边界自动分割：

1. 段落边界（`\n\n`）
2. 行边界（`\n`）
3. 空格边界
4. 硬字节边界（保持 UTF-8 字符完整性）

| 设置 | 说明 | 默认值 |
|------|------|--------|
| `text_chunk_limit` | 每块最大 UTF-8 字节数（>= 100） | `4096` |

## 流式输出

### Webhook 模式

Webhook 模式不支持流式输出。机器人等待 Codex 完整输出后再发送。

### WebSocket 模式

WebSocket 模式通过 `stream` 消息类型支持**流式回复**。当网关的流式模式启用（默认 `partial`）时，Codex 输出以打字机效果实时流式传输：

1. **首个块**到达 → 机器人发送初始流帧（`finish: false`）
2. **后续块** → 机器人发送带累积内容快照的更新帧（`finish: false`）
3. **Codex 完成** → 网关发送最终帧（`finish: true`）

每个流帧包含**完整累积文本**（非增量），企业微信在聊天界面替换显示。

流帧格式：
```json
{
  "cmd": "aibot_respond_msg",
  "headers": {"req_id": "<callback_req_id>"},
  "body": {
    "msgtype": "stream",
    "stream": {
      "id": "clawdex-stream-1",
      "finish": false,
      "content": "累积文本..."
    }
  }
}
```

网关的标准流式节流（1 秒间隔）适用于防止过多帧发送。

## 加密

Webhook 模式下企业微信的消息使用 AES-256-CBC 加密。驱动处理：

- **URL 验证**（GET）：验证 SHA1 签名，解密 echostr，返回明文
- **消息解密**（POST）：验证签名，解密 AES-256-CBC（PKCS7 填充）

加密使用：
- SHA1 签名：`SHA1(sort([token, timestamp, nonce, encrypted]))`
- AES 密钥：`base64decode(EncodingAESKey + "=")`（32 字节）
- IV：AES 密钥的前 16 字节
- 明文格式：16 随机字节 + 4 字节消息长度（大端序）+ 消息 + receiveid

WebSocket 模式不使用 XML 加密——消息通过加密的 WSS 连接以明文 JSON 帧传输。

## 多渠道运行

企业微信可与 Telegram 同时运行。在配置中启用两者：

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "bot_token": "${TELEGRAM_BOT_TOKEN}",
      "enabled": true
    },
    "wecom": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

或只运行企业微信，禁用 Telegram：

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "enabled": false
    },
    "wecom": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

## 机器人命令

命令在私聊和群聊中的可用范围不同：

| 命令 | 私聊 | 群聊 | 说明 |
|------|------|------|------|
| `/help` | 可用 | 可用 | 显示当前聊天场景可用的命令 |
| `/new` | 可用 | 不可用 | 开始新对话 |
| `/sessions` | 可用 | 不可用 | 列出最近会话（最多 10 个） |
| `/resume <id>` | 可用 | 不可用 | 切换到已有会话（完整 ID 或短前缀） |
| `/cancel` | 可用 | 可用 | 取消当前运行中的任务 |
| `/cron help` | 可用 | 可用 | 显示定时任务命令 |
| `/cron list` | 可用 | 可用 | 列出当前聊天的定时任务 |
| `/cron status <id\|序号\|名称>` | 可用 | 可用 | 查看定时任务详情 |
| `/cron stop <id\|序号\|名称>` | 可用 | 可用 | 暂停定时任务 |
| `/cron resume <id\|序号\|名称>` | 可用 | 可用 | 恢复定时任务 |
| `/cron remove <id\|序号\|名称>` | 可用 | 可用 | 删除定时任务 |
| `/cron clear` | 可用 | 可用 | 删除当前聊天的所有定时任务 |
| `/status` | 可用 | 可用 | 显示当前聊天上下文：渠道、作用域、会话、SOUL.md |

群聊里执行 `/new`、`/sessions`、`/resume` 时，网关会直接返回
`not available in group chats`。`/help` 在群聊里也会自动隐藏这些命令。

命令从消息文本内容识别，在 webhook 和 WebSocket 模式下工作方式相同。

### 会话管理

每个聊天维护自己的 Codex 会话。会话持久化到
`~/.local/share/clawdex/sessions.json`。

- `/new` 清除当前会话，下一条消息开始新对话。
- `/sessions` 列出最近会话。Telegram 中显示内联键盘按钮快速恢复。
  企微中会话 ID 以文本显示——复制粘贴 ID 来恢复。
- `/resume <id>` 接受完整 UUID 或短前缀（如 `a3f2`）。
- 会话在网关重启后保持。
- 会话跨渠道共享——在 Telegram 开始的会话可以在企微恢复，反之亦然。

> **注意：** WebSocket 模式下，企业微信会用模板卡片展示命令快捷入口。
> Webhook 模式会退化为纯文本输出；命令仍然可以手动输入执行。

## 配置参考

全局 Codex 配置（例如 `codex.workdir`、`CODEX_WORKDIR`）对所有渠道共用。
如果没有配置 `codex.workdir`，clawdex 会自动创建并使用
`~/.clawdex/workspace`。

> `clawdex config` 使用 `channels.<实例名>.<字段>` 形式的 key。第一个
> 企业微信实例默认名通常是 `wecom`。

### Webhook 模式

| 配置键 | 环境变量 | 说明 | 默认值 |
|--------|----------|------|--------|
| `channels.<name>.enabled` | `WECOM_ENABLED` | 启用企微渠道 | `false` |
| `channels.<name>.connection_mode` | `WECOM_CONNECTION_MODE` | `webhook` 或 `websocket` | `webhook` |
| `channels.<name>.token` | `WECOM_TOKEN` | 回调验证令牌（webhook 必需） | - |
| `channels.<name>.encoding_aes_key` | `WECOM_ENCODING_AES_KEY` | 43 字符 AES 密钥（webhook 必需） | - |
| `channels.<name>.webhook_path` | `WECOM_WEBHOOK_PATH` | HTTP 端点路径（webhook 必需） | - |
| `channels.<name>.text_chunk_limit` | `WECOM_TEXT_CHUNK_LIMIT` | 每块最大 UTF-8 字节（>= 100） | `4096` |
| `channels.<name>.dm_policy` | `WECOM_DM_POLICY` | `open`、`pairing`、`allowlist` | `pairing` |
| `channels.<name>.allow_from` | `WECOM_ALLOW_FROM` | 逗号分隔的 UserID 字符串 | - |
| `channels.<name>.group_allow_from` | `WECOM_GROUP_ALLOW_FROM` | 逗号分隔的群级白名单 ChatID | - |
| `channels.<name>.group_policy` | `WECOM_GROUP_POLICY` | `open`、`allowlist`、`disabled` | `allowlist` |
| `channels.<name>.groups` | *（仅文件）* | 群规则映射（见[群组访问控制](#群组访问控制)） | - |
| `codex.group_sandbox` | `CODEX_GROUP_SANDBOX` | 群消息沙箱级别覆盖 | *（回退到 `codex.sandbox`）* |

### WebSocket 模式（额外字段）

| 配置键 | 环境变量 | 说明 | 默认值 |
|--------|----------|------|--------|
| `channels.<name>.botid` | `WECOM_BOTID` | 机器人 ID（websocket 必需） | - |
| `channels.<name>.secret` | `WECOM_SECRET` | WebSocket 密钥（websocket 必需） | - |
| `channels.<name>.ws_url` | `WECOM_WS_URL` | WebSocket 服务器 URL | `wss://openws.work.weixin.qq.com` |
| `channels.<name>.heartbeat_interval` | `WECOM_HEARTBEAT_INTERVAL` | 心跳 ping 间隔（时长字符串） | `30s` |

> **注意：** `groups` 等结构化字段建议直接编辑
> `~/.clawdex/clawdex.json`。WebSocket 模式下，`token`、
> `encoding_aes_key` 和 `webhook_path` 可选；如果提供，则启用双模式
> 运行（webhook 和 WebSocket 同时生效）。

常见 CLI 用法：

```bash
clawdex config list
clawdex config get channels.wecom.dm_policy
clawdex config set channels.wecom.connection_mode websocket
clawdex config set channels.wecom.group_policy allowlist
clawdex config set channels.wecom.group_allow_from wrkSFfCgAAxxxxxx
clawdex config file
```

## ChatID 映射

企业微信使用字符串格式的 ChatID（如 `wkxxxx`）。内部 clawdex 通过
FNV-64a 哈希转换为 int64，以匹配 `channel.Message.ChatID` 类型。原始
ChatID 会保留用于出站消息路由。

## Webhook URL 缓存

Webhook 模式下，回复 URL 按聊天缓存：

- **消息通知机器人**：每条入站消息包含一个 `WebhookUrl` 用于回复，
  缓存 **2 小时 TTL**。URL 可跨多次回复复用。
- **智能机器人**：每条入站消息包含一次性 `response_url`，缓存
  **1 小时 TTL**。URL 只能调用 **一次**——对同一聊天的后续回复需要新
  的入站消息。这意味着长时间运行的 Codex 任务如果 URL 过期可能无法回复。

缓存在每条新消息到达时刷新。WebSocket 模式下无需 URL 缓存——回复使用
回调的 `req_id` 通过 WebSocket 帧发送。

## 多实例配置

企业微信支持多实例配置，每个实例独立管理连接和访问控制：

```json
{
  "channels": {
    "wecom": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "bot-id-1",
      "secret": "${WECOM_SECRET_1}"
    },
    "wecom-2": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "webhook",
      "token": "${WECOM_TOKEN_2}",
      "encoding_aes_key": "${WECOM_AES_KEY_2}",
      "webhook_path": "/wecom/secondary"
    }
  }
}
```

每个实例可有独立的：
- 连接模式（webhook 或 websocket）。
- 访问控制策略。
- SOUL 提示词（`~/.clawdex/SOUL-<name>.md`）。
- 群组权限配置。
