# clawdex

[中文文档](README_CN.md)

Layered Go gateway that forwards channel messages to native Codex and sends results back.

## Features

- **Multi-Channel Support** — Telegram, WeCom (企业微信), Weixin (微信), QQ Bot, and Feishu with unified message handling
- **Native Codex Integration** — Direct CLI bridge with session persistence
- **Streaming Replies** — Real-time typewriter effect (Telegram partial edits, WeCom WebSocket streams)
- **Access Control** — Pairing codes, allowlists, per-group permissions
- **Media Support** — Images, files, voice messages (channel-dependent)
- **Session Management** — Resume conversations, switch contexts, persistent across restarts
- **Daemon Mode** — Background process with systemd integration
- **Multi-Instance WeCom** — Run multiple WeCom bots from a single gateway

## Getting Started

### Prerequisites

Before installing clawdex, ensure you have:

1. **Go 1.24+** — Required for building from source
2. **Codex CLI** — Install and configure [OpenAI Codex](https://github.com/openai/codex)
   ```bash
   # Verify Codex is working
   codex "What is 2+2?"
   ```
3. **Channel credentials** — Choose one or more:
   - **Telegram**: Bot token from [@BotFather](https://t.me/BotFather)
   - **WeCom**: Token + EncodingAESKey (webhook) or BotID + Secret (websocket)
   - **Weixin**: No pre-setup needed — scan QR code during onboard
   - **QQ Bot**: App ID + Client Secret from [q.qq.com](https://q.qq.com)
   - **Feishu**: App ID + App Secret from [Feishu Open Platform](https://open.feishu.cn)

### Environment Variables

Set up your credentials as environment variables (recommended):

```bash
# Telegram (if using Telegram)
export TELEGRAM_BOT_TOKEN="123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

# WeCom Webhook mode (if using webhook)
export WECOM_TOKEN="your-callback-token"
export WECOM_ENCODING_AES_KEY="your-43-character-encoding-aes-key"
export WECOM_WEBHOOK_PATH="/wecom/webhook"

# WeCom WebSocket mode (if using websocket)
export WECOM_BOTID="your-bot-id"
export WECOM_SECRET="your-websocket-secret"

# QQ Bot (if using QQ)
export QQ_APP_ID="your-app-id"
export QQ_CLIENT_SECRET="your-client-secret"

# Feishu (if using Feishu)
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your-app-secret"
```

You can also add these to `~/.clawdex/env` (loaded by systemd service):

```bash
cat > ~/.clawdex/env << 'EOF'
TELEGRAM_BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
WECOM_BOTID=your-bot-id
WECOM_SECRET=your-websocket-secret
EOF
```

### Installation

```bash
# Install (requires Go 1.24+)
go install github.com/Rememorio/clawdex/cmd/clawdex@latest

# Update the installed executable from the latest GitHub release
clawdex update

# Interactive setup — configures Codex, Telegram, and/or WeCom
clawdex onboard

# Or setup and install daemon in one step (Linux)
clawdex onboard --install-daemon

# Start the gateway
clawdex gateway start
```

That's it. Open your Telegram bot (or WeCom group) and send a message.

### Quick Configuration

```bash
clawdex config list                         # Show all config values
clawdex config get <KEY>                    # Get a config value
clawdex config set <KEY> <VALUE>            # Set a config value
clawdex config file                         # Show config file path
```

## Daemon Management

```bash
clawdex daemon install      # Install systemd user service (Linux)
clawdex daemon uninstall    # Remove systemd user service
clawdex update              # Update the current executable
clawdex gateway start       # Start as background daemon
clawdex gateway run         # Run in foreground (useful for debugging)
clawdex gateway status      # Show process status
clawdex gateway stop        # Stop the daemon
clawdex gateway restart     # Restart the daemon
```

### systemd User Service (Linux)

`daemon install` registers clawdex as a systemd user service with automatic restart and boot autostart — no root required:

```bash
clawdex gateway install
```

This creates `~/.config/systemd/user/clawdex-gateway.service`, enables it, and starts it immediately. The service restarts on failure (`RestartSec=5`) and survives logout via `loginctl enable-linger`. Environment variables can be set in `~/.clawdex/env`.

View logs:

```bash
journalctl --user -u clawdex-gateway -f
```

To remove:

```bash
clawdex gateway uninstall
```

## Configuration

Config is stored in `~/.clawdex/clawdex.json`. All settings can also be overridden with environment variables. Run `clawdex onboard` for interactive setup.

### Configuration File Structure

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
    "weixin": {
      "type": "weixin",
      "enabled": true,
      "dm_policy": "open"
    },
    "qqbot": {
      "type": "qqbot",
      "enabled": true,
      "app_id": "${QQ_APP_ID}",
      "client_secret": "${QQ_CLIENT_SECRET}",
      "dm_policy": "open",
      "group_policy": "open"
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

If `codex.workdir` is not configured, clawdex automatically creates and uses
`~/.clawdex/workspace`.

### Wildcard Configuration

Use `channels.*.<field>` to set a field for all channels:

```bash
clawdex config set 'channels.*.allow_from' 'YOUR_USER_ID'
```

## User Approval (Pairing)

By default, unknown users receive a pairing code. Approve them via CLI:

```bash
clawdex pairing list              # List pending requests
clawdex pairing approve <CODE>    # Approve a user
```

See [docs/TELEGRAM.md](docs/TELEGRAM.md) for other access control modes.

## Bot Commands

Available in Telegram, WeCom, Weixin, QQ Bot, and Feishu:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/new` | Start a fresh conversation |
| `/sessions` | List recent sessions (up to 10) |
| `/resume <id>` | Switch to an existing session |
| `/cancel` | Cancel the running task |
| `/status` | Show current chat context: channel, scope, session, SOUL.md |
| `/cron list` | List scheduled jobs for the current chat |
| `/cron status <id\|index\|name>` | Show a scheduled job |
| `/cron stop <id\|index\|name>` | Disable a scheduled job |
| `/cron resume <id\|index\|name>` | Re-enable a scheduled job |
| `/cron remove <id\|index\|name>` | Delete a scheduled job |
| `/cron clear` | Delete all scheduled jobs for the current chat |

## Scheduled Jobs

clawdex can create reminders and recurring jobs from natural language. When
Codex sees a concrete time, interval, cadence, or cron expression in a chat
request, it can call the built-in `clawdex_cron` MCP tool to create the job for
that same chat. Jobs persist under `~/.clawdex/cron/jobs.json` by default.

The scheduler supports one-shot RFC3339 times, fixed intervals, and five-field
cron expressions with optional IANA time zones. Fixed reminders send stored
text; agent jobs run Codex again at schedule time and send the fresh result
back to the originating chat.

Runtime settings:

| Config key | Env var | Default | Description |
|------------|---------|---------|-------------|
| `cron.enabled` | `CRON_ENABLED` | `true` | Enable the scheduler |
| `cron.store` | `CRON_STORE` | `cron/jobs.json` | Job store path, relative to `~/.clawdex` unless absolute |
| `cron.mcp_enabled` | `CRON_MCP_ENABLED` | `true` | Expose the cron MCP tool to Codex |

`clawdex mcp-server cron` is the stdio MCP endpoint used by Codex through the
gateway-issued context token. It is not normally run by hand.

## SOUL.md

Inject a system prompt into every Codex session by creating `~/.clawdex/SOUL.md`:

```bash
cat > ~/.clawdex/SOUL.md << 'EOF'
You are a helpful coding assistant.
EOF
```

SOUL files are reloaded when a fresh Codex session starts. After editing a SOUL
file, use `/new` in chat for the next message to pick up the change.

For multi-instance WeCom setups, use `~/.clawdex/SOUL-<name>.md` for per-instance prompts.

## Diagnostics

```bash
clawdex doctor            # Check configuration health
clawdex doctor --fix      # Check and auto-fix problems
```

## Documentation

- [Telegram Channel](docs/TELEGRAM.md) — configuration reference, access control, streaming, media, commands.
- [WeCom Channel](docs/WECOM.md) — WeCom (企业微信) setup, encryption, webhook configuration, multi-instance.
- [Weixin Channel](docs/WEIXIN.md) — Weixin (微信) personal WeChat setup, QR login, typing indicators, media.
- [QQ Bot Channel](docs/QQBOT.md) — QQ Bot setup, WebSocket gateway, group @-mentions, media upload.
- [Feishu Channel](docs/FEISHU.md) — Feishu long connection setup, receive-message events, access control.
- [Scheduled Jobs](docs/CRON.md) — natural-language reminders, recurring jobs, and cron configuration.

## Architecture

```
cmd/clawdex          CLI entry point
internal/
  app/               CLI command handlers (gateway, config, pairing)
  channel/           Channel abstraction (Driver, Responder, Message)
  channel/telegram/  Telegram long-polling driver
  channel/wecom/     WeCom webhook & WebSocket driver
  channel/weixin/    Weixin personal WeChat long-polling driver
  channel/qqbot/     QQ Bot WebSocket driver
  channel/feishu/    Feishu long-connection driver
  gateway/           Message orchestration, worker pool, slash commands
  codex/             Native Codex CLI bridge (codex exec)
  cron/              Persistent scheduled jobs
  mcp/               Local MCP tools exposed to Codex
  config/            Configuration loading (file + env)
  onboard/           Interactive setup wizard
  server/            HTTP server (/healthz)
  daemon/            PID-file based process lifecycle + systemd install
  doctor/            Configuration health checks
  pairing/           Pairing code store
  secret/            Secret reference resolution (${ENV}, file://)
  logger/            Structured logging with slog
```

## Development

```bash
# Run tests
go test ./...

# Build
go build ./cmd/clawdex

# Run in foreground with debug logging
clawdex gateway run
```

## License

MIT License. See [LICENSE](LICENSE).
