# Scheduled Jobs

clawdex can persist reminders and recurring work for any chat. The intended
flow is natural language: when a user asks for a concrete future time, interval,
cadence, or cron expression, Codex calls the built-in `clawdex_cron` MCP tool
and the gateway creates the job for the current chat.

## How It Works

1. A chat message reaches the gateway.
2. The gateway starts Codex with a short-lived cron context token.
3. Codex can call the local MCP server (`clawdex mcp-server cron`).
4. The MCP server posts the request back to the gateway at `/cron/tool`.
5. The gateway validates that the job belongs to the current chat and persists
   it in the cron store.

Users normally do not run `clawdex mcp-server cron` directly. It is started by
Codex from the MCP configuration injected into each gateway-run Codex process.

## Creating Jobs

Ask naturally in the chat where the result should be delivered:

```text
Remind me tomorrow at 9:00 to review the deploy checklist.
Every weekday at 18:00, ask Codex to summarize open release tasks.
Every 30 minutes remind me to check the migration dashboard.
At 2026-06-12T09:00:00+08:00 send "standup starts".
```

Codex should only create a job when the request includes a concrete schedule.
If the time is ambiguous, Codex should ask a follow-up question instead of
guessing.

## Managing Jobs

Use `/cron` commands in the same chat that owns the job:

| Command | Description |
|---------|-------------|
| `/cron list` | List scheduled jobs for the current chat |
| `/cron status <id\|index\|name>` | Show a job |
| `/cron stop <id\|index\|name>` | Disable a job |
| `/cron resume <id\|index\|name>` | Re-enable a job |
| `/cron remove <id\|index\|name>` | Delete a job |
| `/cron clear` | Delete all jobs for the current chat |

The `index` is the number shown by `/cron list`. IDs can be abbreviated with
the short ID displayed in the list.

## Schedules

clawdex supports three schedule kinds:

| Kind | Fields | Behavior |
|------|--------|----------|
| `at` | `at` | One-shot RFC3339 timestamp. The job disables itself after it runs. |
| `every` | `every_seconds`, optional `anchor` | Fixed interval in seconds. `anchor` is RFC3339. |
| `cron` | `expr`, optional `timezone` | Five-field cron expression: minute hour day month weekday. |

Cron expressions use numeric fields only. Weekday `0` and `7` both mean
Sunday. If `timezone` is omitted, the gateway's local timezone is used; prefer
an explicit IANA timezone such as `Asia/Shanghai` or `UTC` when asking for a
calendar-based schedule.

## Payloads

| Kind | Behavior |
|------|----------|
| `message` | Sends the stored text at run time. Use this for fixed reminders. |
| `agent` | Runs Codex again at run time using the stored instruction, then sends the fresh result. |

Agent jobs use a stable session scope per scheduled job. Repeated runs of the
same job can keep continuity, but they are isolated from the live chat session
that created the job so manual runs cannot collide with an in-flight chat turn.
If an agent run fails before producing a report, clawdex records the job as
failed and sends a failure notice to the job's delivery target when possible.

When an agent job needs multiple pushed messages, Codex returns a structured
delivery envelope and clawdex sends each entry to the originating chat in order.
This avoids depending on an external delivery tool while still supporting
long reports split into multiple pushes. If a delivery envelope is used,
clawdex marks the run as delivered and does not send an extra final message.

## Configuration

Cron is enabled by default.

```json
{
  "cron": {
    "enabled": true,
    "store": "cron/jobs.json",
    "mcp_enabled": true
  }
}
```

| Config key | Env var | Default | Description |
|------------|---------|---------|-------------|
| `cron.enabled` | `CRON_ENABLED` | `true` | Enable the scheduler |
| `cron.store` | `CRON_STORE` | `cron/jobs.json` | Job store path, relative to `~/.clawdex` unless absolute |
| `cron.mcp_enabled` | `CRON_MCP_ENABLED` | `true` | Expose the cron MCP tool to Codex |

Config can also be changed with:

```bash
clawdex config set cron.enabled true
clawdex config set cron.store cron/jobs.json
clawdex config set cron.mcp_enabled true
```

## Channel Support

Scheduled delivery uses each channel's proactive send implementation.

| Channel | Support |
|---------|---------|
| Telegram | Sends to the original chat or thread |
| Weixin | Sends to the original user |
| QQ Bot | Sends to the original C2C or group openid |
| Feishu | Sends to the original chat |
| WeCom | Best-effort via the cached response route for recent inbound chats |

WeCom bot APIs are more constrained than the other channels. If the platform no
longer accepts a cached response route for a scheduled job, delivery can fail
and the job's last error will record the failure.

## Troubleshooting

### Codex does not create a job

- Confirm `cron.enabled` and `cron.mcp_enabled` are both `true`.
- Use a concrete schedule in the prompt.
- Check gateway logs for `cron context created` and MCP tool errors.

### A job does not run

- Run `/cron list` in the owning chat and check `next=...`.
- Run `/cron status <id>` and inspect the last status and run count.
- Confirm the gateway process is running; jobs are executed by the gateway.

### Delivery fails

- Check `/cron status <id>` for the last error.
- Confirm the original channel instance is still enabled.
- For WeCom, send a fresh message in the chat and try again if the cached
  response route expired.
