# Telegram 渠道

clawdex Telegram 渠道的详细参考文档。

## 机器人设置

1. 通过 [BotFather](https://t.me/BotFather) 创建机器人（`/newbot`）。
2. 复制机器人令牌。
3. 运行 `clawdex onboard` 并按提示粘贴令牌。

令牌保存到 `~/.clawdex/clawdex.json`，支持三种格式：

| 格式 | 示例 | 说明 |
|------|------|------|
| 明文字符串 | `123456:ABC-DEF` | 直接存储 |
| 环境变量引用 | `${TELEGRAM_BOT_TOKEN}` | 运行时从环境变量解析 |
| 文件引用 | `file:///run/secrets/token` | 运行时从文件读取 |

### 推荐：环境变量

`clawdex onboard` 向导默认使用环境变量引用。启动网关前设置：

```bash
export TELEGRAM_BOT_TOKEN="123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
```

配置文件中将包含 `${TELEGRAM_BOT_TOKEN}`，运行时解析。这样可以避免在磁盘上明文存储令牌。

## 访问控制

通过 `dm_policy`（配置文件）或 `TELEGRAM_DM_POLICY`（环境变量）控制。

| 策略 | 行为 |
|------|------|
| `pairing`（默认） | 未知用户收到配对码，管理员通过 CLI 审批 |
| `allowlist` | 只有 `allow_from` 列表中的用户 ID 可以交互 |
| `open` | 任何人都可以向机器人发送消息 |

### 配对流程

1. 用户向机器人发送任意消息。
2. 机器人回复一个 6 位配对码（例如 `A3X9K2`）。
3. 管理员运行：
   ```bash
   clawdex pairing approve A3X9K2
   ```
4. 用户 ID 被添加到运行时白名单，**同时**持久化到配置文件的
   `allow_from` 中。变更即时生效，无需重启网关。
5. 机器人通知用户："You have been approved!"

管理命令：

```bash
clawdex pairing list              # 列出待处理的配对请求
clawdex pairing approve <CODE>    # 审批请求
```

### 白名单

将 `dm_policy` 设为 `"allowlist"` 并列出用户 ID：

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "dm_policy": "allowlist",
      "allow_from": [123456789, 987654321]
    }
  }
}
```

`clawdex onboard` 创建的第一个 Telegram 实例默认名为 `telegram`。
如果你保留默认名，可直接执行：

```bash
clawdex config set channels.telegram.dm_policy allowlist
clawdex config set channels.telegram.allow_from 123456789,987654321
```

用户可以从网关日志里查看自己的 ID。给机器人发送私聊消息后，查找
`telegram recv` 日志行，并读取其中的 `sender_id` 字段。

## 群聊支持

clawdex 支持 Telegram 群组和超级群。群消息由 `group_policy` 控制：

| 策略 | 行为 |
|------|------|
| `allowlist`（默认） | 只有白名单中的群可使用机器人 |
| `open` | 所有群都可使用机器人 |
| `disabled` | 忽略所有群消息 |

### 群聊配置

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "bot_token": "${TELEGRAM_BOT_TOKEN}",
      "group_policy": "allowlist",
      "group_allow_from": [-1001234567890, -1009876543210],
      "groups": {
        "-1001234567890": {
          "enabled": true,
          "allow_from": [123456789],
          "require_mention": false
        }
      },
      "require_mention": true
    }
  }
}
```

如果你使用 CLI，常见群聊配置命令如下：

```bash
clawdex config set channels.telegram.group_policy allowlist
clawdex config set channels.telegram.group_allow_from -1001234567890
clawdex config set channels.telegram.require_mention true
```

> `groups` 是结构化映射，建议直接编辑 `~/.clawdex/clawdex.json`。

### 群聊访问层级

1. **全局群白名单**：`group_allow_from` 控制允许接入的群 ChatID。
2. **群级规则**：`groups.<chat_id>` 可进一步控制：
   - `enabled`：是否启用该群。
   - `allow_from`：限制群内允许触发机器人的用户 ID。
   - `require_mention`：覆盖全局 `require_mention`。

### @ 提及规则

默认情况下，机器人只会响应群里 **@ 到机器人** 的消息。这样可以避免
在活跃群里对每条消息都回复。

- 全局默认：`require_mention: true`。
- 单群覆盖：`groups.<chat_id>.require_mention: false`。

当 `require_mention` 打开时，群里的普通消息和斜杠命令都需要先满足提及
规则，网关才会继续处理。

### 获取群聊 ID

1. 把机器人拉进目标群。
2. 在群里发送任意一条消息。
3. 查看网关输出中的 `telegram recv` 或 `telegram skip` 日志行。
4. 复制其中的 `chat_id` 字段。超级群通常形如 `-100xxxxxxxxxx`。

即使当前消息因为 `group_policy`、`group_allow_from` 或
`require_mention` 被拦截，日志里仍然会打印同一个 `chat_id`。

## 流式输出

通过 `streaming`（配置）或 `TELEGRAM_STREAMING`（环境变量）控制。

| 模式 | 行为 |
|------|------|
| `partial`（默认） | 机器人发送初始消息，随输出流式更新编辑该消息 |
| `progress` | 同 `partial`（别名） |
| `off` | 机器人等待完整输出，然后发送单条消息 |

## 消息分块

长消息在发送前自动分割。通过 `chunk_mode` 和 `text_chunk_limit` 控制。

| 设置 | 说明 | 默认值 |
|------|------|--------|
| `chunk_mode` | `"length"`（硬切分）或 `"newline"`（按段落边界分割） | `length` |
| `text_chunk_limit` | 每块最大字符数（>= 100） | `3500` |

`newline` 模式下，分割器优先尝试 `\n\n` 边界，然后是 `\n`，然后是空格，最后才是硬切分。

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

命令会在启动时自动注册到 Telegram 命令菜单。若当前群开启了
`require_mention`，群里的命令同样需要先 @ 机器人。

## 会话管理

每个聊天维护自己的会话。会话持久化到 `~/.local/share/clawdex/sessions.json`。

- `/new` 清除当前会话，下一条消息开始新对话。
- `/sessions` 列出最近会话，带内联键盘按钮快速恢复。
- `/resume <id>` 接受完整 UUID 或短前缀（如 `a3f2`）。
- 会话在网关重启后保持。

## 媒体

### 入站（用户 -> 机器人）

机器人下载以下媒体类型并作为 `--image` 参数转发给 Codex：

- 照片（最大可用尺寸）
- 静态贴纸（WEBP）

其他媒体类型（视频、音频、语音、文档、动态/视频贴纸）以文本占位符表示（如 `[video]`、`[audio]`、`[document: file.pdf]`）。

### 出站（机器人 -> 用户）

当 Codex 输出包含文件路径时，网关检测并作为附件发送：

- 图片：`.jpg`、`.jpeg`、`.png`、`.gif`、`.webp`
- 语音：`.ogg`、`.oga`
- 文档：其他所有（`.pdf`、`.txt`、`.zip` 等）

标题限制为 1024 字符（Telegram 限制），超出部分作为单独的文本消息发送。

## 格式化

Codex 输出（Markdown）在发送前转换为 Telegram HTML：

- `**bold**` -> `<b>bold</b>`
- `*italic*` / `_italic_` -> `<i>italic</i>`
- `` `code` `` -> `<code>code</code>`
- 代码块 -> `<pre><code class="language-x">...</code></pre>`
- `~~strike~~` -> `<s>strike</s>`
- `> quote` -> 可展开引用块
- `[text](url)` -> `<a href="url">text</a>`
- 思考标签（`<thinking>...</thinking>`） -> 可展开引用块

Codex 输出中的原始 HTML 会被转义以防止 Telegram 解析错误。如果 HTML 解析仍失败，消息会以纯文本重试。

链接预览默认启用。当机器人发送包含 URL 的消息时，Telegram 会在消息下方显示富预览。

## SOUL.md

创建 `~/.clawdex/SOUL.md` 可向每次 Codex 会话注入系统提示词：

```bash
cat > ~/.clawdex/SOUL.md << 'EOF'
你是一个有帮助的编程助手。
EOF
```

文件内容在每次调用时作为 `--instructions` 传递给 Codex。无需修改配置——网关会自动加载 `~/.clawdex/SOUL.md`（如果存在）。

## 健康检查

网关暴露一个 HTTP 服务器（默认 `:8080`），提供 `/healthz` 端点：

```bash
curl -sS http://127.0.0.1:8080/healthz
```

## 配置参考

所有设置可在 `~/.clawdex/clawdex.json` 中设置，或通过环境变量覆盖。

### Telegram 设置

> `clawdex config` 使用 `channels.<实例名>.<字段>` 形式的 key。第一个
> Telegram 实例默认名通常是 `telegram`。

| 配置键 | 环境变量 | 说明 | 默认值 |
|--------|----------|------|--------|
| `channels.<name>.bot_token` | `TELEGRAM_BOT_TOKEN` | 机器人令牌（必需） | - |
| `channels.<name>.enabled` | `TELEGRAM_ENABLED` | 启用/禁用 Telegram | `true` |
| `channels.<name>.dm_policy` | `TELEGRAM_DM_POLICY` | `open`、`pairing`、`allowlist` | `pairing` |
| `channels.<name>.allow_from` | `TELEGRAM_ALLOW_FROM` | 逗号分隔的用户 ID | - |
| `channels.<name>.group_policy` | `TELEGRAM_GROUP_POLICY` | `open`、`allowlist`、`disabled` | `allowlist` |
| `channels.<name>.group_allow_from` | `TELEGRAM_GROUP_ALLOW_FROM` | 逗号分隔的群 ChatID | - |
| `channels.<name>.require_mention` | - | 群里是否要求 @ 机器人 | `true` |
| `channels.<name>.streaming` | `TELEGRAM_STREAMING` | `off`、`partial`、`progress` | `partial` |
| `channels.<name>.chunk_mode` | `TELEGRAM_CHUNK_MODE` | `length`、`newline` | `length` |
| `channels.<name>.text_chunk_limit` | `TELEGRAM_TEXT_CHUNK_LIMIT` | 每块最大字符数（>= 100） | `3500` |
| `channels.<name>.groups` | *（仅文件）* | 单群规则映射 | - |
| `channels.<name>.poll_timeout` | `TELEGRAM_POLL_TIMEOUT` | 长轮询秒数（1-50） | `30` |
| `channels.<name>.startup_probe_timeout` | `TELEGRAM_STARTUP_PROBE_TIMEOUT` | 启动探针超时 | `8s` |

### Codex 设置

| 配置键 | 环境变量 | 说明 | 默认值 |
|--------|----------|------|--------|
| `codex.workdir` | `CODEX_WORKDIR` | Codex 工作目录 | `~/.clawdex/workspace` |
| `codex.timeout` | `CODEX_TIMEOUT` | 每次请求超时（时长字符串） | `120m` |
| `codex.sandbox` | `CODEX_SANDBOX` | 沙箱级别 | `workspace-write` |

### 服务器设置

| 配置键 | 环境变量 | 说明 | 默认值 |
|--------|----------|------|--------|
| `gateway.address` | `GATEWAY_ADDR` | HTTP 监听地址 | `:8080` |

### 配置示例

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "bot_token": "${TELEGRAM_BOT_TOKEN}",
      "dm_policy": "pairing",
      "group_policy": "allowlist",
      "streaming": "partial",
      "chunk_mode": "newline",
      "text_chunk_limit": 4000
    }
  },
  "codex": {
    "workdir": "/home/user/project",
    "sandbox": "workspace-write",
    "timeout": "30m"
  }
}
```

你也可以使用 `clawdex config` 命令进行非交互式管理：

```bash
clawdex config list                                  # 显示所有配置值
clawdex config get channels.telegram.dm_policy       # 获取特定值
clawdex config set channels.telegram.dm_policy allowlist
clawdex config set channels.telegram.group_policy allowlist
clawdex config set channels.telegram.group_allow_from -1001234567890
clawdex config file                                  # 显示配置文件路径
```
