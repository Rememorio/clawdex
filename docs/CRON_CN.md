# 定时任务

clawdex 可以为任意聊天持久化提醒和周期任务。推荐使用方式是自然语言：当用户提出明确的未来时间、间隔、周期或 cron 表达式时，Codex 会调用内置的 `clawdex_cron` MCP tool，网关会把任务创建到当前聊天。

## 工作方式

1. 聊天消息进入 gateway。
2. gateway 启动 Codex，并注入一个短期有效的 cron 上下文令牌。
3. Codex 可以调用本地 MCP server（`clawdex mcp-server cron`）。
4. MCP server 把请求回传给 gateway 的 `/cron/tool`。
5. gateway 校验任务属于当前聊天，然后持久化到 cron store。

用户通常不需要手动运行 `clawdex mcp-server cron`。它会由 gateway 启动的 Codex 进程通过注入的 MCP 配置自动调用。

## 创建任务

在需要接收结果的聊天里直接自然语言描述：

```text
明天早上 9 点提醒我检查发布 checklist。
每个工作日 18:00，让 Codex 总结一下未完成的发版任务。
每 30 分钟提醒我看一次迁移 dashboard。
在 2026-06-12T09:00:00+08:00 发送“standup 开始了”。
```

Codex 只应在请求包含明确调度时间时创建任务。如果时间不明确，应先追问，而不是猜测。

## 管理任务

在任务所属的同一个聊天里使用 `/cron` 命令：

| 命令 | 说明 |
|------|------|
| `/cron list` | 列出当前聊天的定时任务 |
| `/cron status <id\|序号\|名称>` | 查看任务详情 |
| `/cron stop <id\|序号\|名称>` | 暂停任务 |
| `/cron resume <id\|序号\|名称>` | 恢复任务 |
| `/cron remove <id\|序号\|名称>` | 删除任务 |
| `/cron clear` | 删除当前聊天的所有任务 |

`序号` 是 `/cron list` 显示的编号。ID 可以使用列表里显示的短 ID。

## 调度格式

clawdex 支持三种调度类型：

| 类型 | 字段 | 行为 |
|------|------|------|
| `at` | `at` | RFC3339 一次性时间。运行后任务会自动禁用。 |
| `every` | `every_seconds`，可选 `anchor` | 按秒数固定间隔运行。`anchor` 为 RFC3339。 |
| `cron` | `expr`，可选 `timezone` | 五段 cron 表达式：分 时 日 月 周。 |

cron 表达式只支持数字字段。星期字段里 `0` 和 `7` 都表示周日。如果不传 `timezone`，会使用 gateway 进程本地时区；对于日历类任务，建议明确指定 IANA 时区，例如 `Asia/Shanghai` 或 `UTC`。

## Payload 类型

| 类型 | 行为 |
|------|------|
| `message` | 到点发送保存的文本。适合固定提醒。 |
| `agent` | 到点重新运行 Codex，用保存的指令生成新结果，再发回聊天。 |

agent 任务使用每个定时任务独立且稳定的 session scope。同一个任务的多次执行可以保持连续性，但会和创建任务时的实时聊天 session 隔离，避免手动立即执行时和正在进行的聊天轮次冲突。如果 agent 任务在产出报告前失败，clawdex 会把任务状态记为失败，并在可能时向任务投递目标发送失败通知。

agent 任务需要多批推送时，Codex 会返回结构化的投递 envelope，clawdex 负责按顺序把每一条发送到创建任务的聊天。这不依赖外部通知工具，也能支持长报告拆成多条消息。只要使用了投递 envelope，clawdex 会把本次运行标记为已投递，并且不会再额外发送最终回复。

## 配置

cron 默认启用。

```json
{
  "cron": {
    "enabled": true,
    "store": "cron/jobs.json",
    "mcp_enabled": true
  }
}
```

| 配置项 | 环境变量 | 默认值 | 说明 |
|--------|----------|--------|------|
| `cron.enabled` | `CRON_ENABLED` | `true` | 启用调度器 |
| `cron.store` | `CRON_STORE` | `cron/jobs.json` | 任务存储路径；相对路径基于 `~/.clawdex` |
| `cron.mcp_enabled` | `CRON_MCP_ENABLED` | `true` | 向 Codex 暴露 cron MCP tool |

也可以通过 CLI 修改：

```bash
clawdex config set cron.enabled true
clawdex config set cron.store cron/jobs.json
clawdex config set cron.mcp_enabled true
```

## 渠道支持

定时投递依赖各渠道的主动发送实现。

| 渠道 | 支持情况 |
|------|----------|
| Telegram | 发送到原聊天或原 thread |
| Weixin | 发送到原用户 |
| QQ Bot | 发送到原 C2C 或群 openid |
| Feishu | 发送到原 chat |
| WeCom | 基于近期入站聊天缓存响应通道的 best-effort 投递 |

WeCom 机器人 API 的主动投递限制比其他渠道更多。如果平台不再接受缓存响应通道，定时任务投递可能失败，任务的 last error 会记录失败原因。

## 排查问题

### Codex 没有创建任务

- 确认 `cron.enabled` 和 `cron.mcp_enabled` 都是 `true`。
- 在 prompt 里给出明确的调度时间。
- 查看 gateway 日志里是否有 `cron context created` 和 MCP tool 错误。

### 任务没有运行

- 在任务所属聊天里执行 `/cron list`，检查 `next=...`。
- 执行 `/cron status <id>`，查看 last status 和 run count。
- 确认 gateway 进程正在运行；任务由 gateway 执行。

### 投递失败

- 用 `/cron status <id>` 查看 last error。
- 确认原渠道实例仍然启用。
- 对 WeCom，如果缓存响应通道过期，先在原聊天发送一条新消息，再重试任务。
