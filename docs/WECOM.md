# WeCom (企业微信) Channel

Detailed reference for the WeCom channel in clawdex.

## Overview

WeCom (企业微信) support provides two connection modes:

- **Webhook mode** (default) — WeCom pushes encrypted messages to an HTTP endpoint. Supports both **notification bots** (消息通知机器人, XML format) and **AI bots** (智能机器人, JSON format). Requires `token` and `encoding_aes_key`.
- **WebSocket mode** — connects to WeCom via a persistent WebSocket connection (`wss://openws.work.weixin.qq.com`). Only requires `botid` and `secret`. Supports **streaming replies** (typewriter effect).

## Bot Types

WeCom has two types of group bots:

| | Notification Bot (消息通知机器人) | AI Bot (智能机器人) |
|---|---|---|
| Connection modes | Webhook only | Webhook or WebSocket |
| Message encryption envelope | XML | JSON |
| Decrypted message format | XML | JSON |
| Reply mechanism | Fixed `webhook_url` (reusable) | One-shot `response_url` (single use, 1h expiry) |
| Streaming support | No | Yes (WebSocket mode only) |
| Setup credentials | Token + EncodingAESKey | Token + EncodingAESKey (webhook) or BotID + Secret (websocket) |

Both bot types are supported in webhook mode — the driver auto-detects the envelope format (XML vs JSON) and parses accordingly.

## Bot Setup

### Webhook Mode (default)

1. Create a bot in WeCom (企业微信) — either a **notification bot** (消息通知机器人) or an **AI bot** (智能机器人) — and configure the callback URL.
2. From the bot admin page, copy:
   - **Token** — callback verification token
   - **EncodingAESKey** — 43-character AES encryption key
3. Add the configuration to `~/.clawdex/clawdex.json`:

```json
{
  "channels": {
    "wecom-bot": {
      "type": "wecom",
      "enabled": true,
      "token": "your-callback-verification-token",
      "encoding_aes_key": "your-43-character-encoding-aes-key",
      "webhook_path": "/wecom/webhook"
    }
  }
}
```

4. Set the bot callback URL to `https://your-server:8080/wecom/webhook`.

Or use the non-interactive CLI:

```bash
clawdex config set channels.wecom-bot.enabled true
clawdex config set channels.wecom-bot.token '${WECOM_TOKEN}'
clawdex config set channels.wecom-bot.encoding_aes_key '${WECOM_ENCODING_AES_KEY}'
clawdex config set channels.wecom-bot.webhook_path /wecom/webhook
```

### WebSocket Mode

1. Create an AI bot (智能机器人) in WeCom admin.
2. From the bot admin page, copy:
   - **BotID** — the bot's unique identifier
   - **Secret** — the WebSocket connection secret
3. Add the configuration to `~/.clawdex/clawdex.json`:

```json
{
  "channels": {
    "wecom-ai-bot": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

No HTTP callback URL is needed — the bot connects outbound to WeCom's WebSocket server.

Or use the non-interactive CLI:

```bash
clawdex config set channels.wecom-ai-bot.enabled true
clawdex config set channels.wecom-ai-bot.connection_mode websocket
clawdex config set channels.wecom-ai-bot.botid your-bot-id
clawdex config set channels.wecom-ai-bot.secret '${WECOM_SECRET}'
```

### Token and Secret Formats

Token, encoding_aes_key, and secret values support the same formats:

| Format | Example | Description |
|--------|---------|-------------|
| Plain string | `abc123...` | Stored directly |
| Env var ref | `${WECOM_TOKEN}` | Resolved from environment at runtime |
| File ref | `file:///run/secrets/wecom_token` | Read from file at runtime |

### Recommended: Environment Variables

The `clawdex onboard` wizard defaults to environment variable references. Set the following before starting the gateway:

**Webhook mode:**
```bash
export WECOM_TOKEN="your-callback-verification-token"
export WECOM_ENCODING_AES_KEY="your-43-character-encoding-aes-key"
```

**WebSocket mode:**
```bash
export WECOM_SECRET="your-websocket-secret"
```

The config file will then contain `${WECOM_TOKEN}`, `${WECOM_ENCODING_AES_KEY}`, or `${WECOM_SECRET}`, which are resolved at runtime. This avoids storing secrets in plaintext on disk.

## Connection Modes

### Webhook

The default mode. WeCom pushes encrypted messages to an HTTP endpoint. The gateway auto-detects the envelope format:

- **Notification bot** (消息通知机器人): XML envelope → XML body, replies via reusable `webhook_url`
- **AI bot** (智能机器人): JSON envelope → JSON body, replies via one-shot `response_url`

Requirements:
- `token`, `encoding_aes_key`, `webhook_path`
- HTTP route is registered on the gateway server
- No streaming support in webhook mode (neither bot type supports message editing via HTTP)

### WebSocket

The bot connects to `wss://openws.work.weixin.qq.com` and maintains a persistent connection. Messages arrive as JSON frames via `aibot_msg_callback` commands. Replies are sent as `aibot_respond_msg` frames.

- Requires: `botid`, `secret`
- No HTTP callback URL needed — no inbound HTTP route is registered
- Supports **streaming replies** via `msgtype: "stream"`
- Auto-reconnects on connection loss (3s delay)
- Heartbeat ping every 30s (configurable via `heartbeat_interval`)

#### WebSocket Protocol

The WebSocket connection uses a JSON frame protocol:

```
→ aibot_subscribe   {bot_id, secret}      # subscribe on connect
→ ping              {}                      # heartbeat (every 30s)
← aibot_msg_callback    {message JSON}     # inbound user message
← aibot_event_callback  {event JSON}       # inbound event (ignored)
→ aibot_respond_msg     {msgtype, markdown/stream}  # reply
→ aibot_send_msg        {chatid, msgtype, markdown}  # proactive send
```

Each frame has a `cmd` field and `headers.req_id`. The `req_id` from an inbound callback **must** be echoed back in the reply frame.
Proactive sends generate a new local `req_id` and use `aibot_send_msg`, so they
do not depend on a cached callback `req_id`. Scheduled WebSocket delivery stores
group chats as `group:<chatid>` and single chats as `single:<userid>`; the
`chatid` field in the proactive send frame receives the resolved chat ID or user
ID.

## Access Control

Controlled by `dm_policy` (config file) or `WECOM_DM_POLICY` (env).

| Policy | Behavior |
|--------|----------|
| `pairing` (default) | Unknown users receive a pairing code; admin approves via CLI |
| `open` | All users can interact with the bot |
| `allowlist` | Only UserIDs listed in `allow_from` can interact |

### Pairing Flow

1. User sends a message to the bot.
2. Bot replies with a 6-character pairing code (e.g. `A3X9K2`).
3. Admin runs:
   ```bash
   clawdex pairing approve A3X9K2
   ```
4. The UserID is added to the runtime allow list **and** persisted to
   `allow_from` in the config file. The change takes effect immediately
   — no gateway restart is needed.

### Allowlist

Set `dm_policy` to `"allowlist"` and list UserIDs:

```json
{
  "channels": {
    "wecom-bot": {
      "type": "wecom",
      "dm_policy": "allowlist",
      "wecom_allow_from": ["user1", "user2"]
    }
  }
}
```

## Finding ChatID and UserID

WeCom does not expose ChatID or UserID in the admin console. The easiest way to obtain them is from the gateway log output:

1. Temporarily set `group_policy` to `"open"` so all group messages are accepted:
```bash
clawdex config set channels.wecom-bot.group_policy open
clawdex gateway restart
```

2. Send a message in the target group (make sure to @mention the bot).

3. Check the gateway log — each incoming message prints:
   ```
   wecom recv: type=text from=zhangsan chat=wrkSFfCgAAxxxxxx
   ```
   In WebSocket mode the log line is:
   ```
   wecom websocket recv: type=text from=zhangsan chat=wrkSFfCgAAxxxxxx req_id=...
   ```
   - `from=` is the **UserID** (for the `allow_from` allowlist)
   - `chat=` is the **ChatID** (for the `groups` map key or `group_allow_from` list)

4. Copy the values into your config, then switch back to `"allowlist"`:
```json
{
  "channels": {
    "wecom-bot": {
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

View logs with:

```bash
# Foreground mode
clawdex gateway run

# systemd service
journalctl --user -u clawdex-gateway -f

# Background daemon
tail -f ~/.clawdex/gateway.log
```

> **Tip:** You can also use `"*": {}` as a temporary wildcard to accept all groups while collecting IDs, then replace it with specific entries.

## Group Access Control

Group messages are controlled by `group_policy` (config file) or `WECOM_GROUP_POLICY` (env).

| Policy | Behavior |
|--------|----------|
| `allowlist` (default) | Only groups listed in `group_allow_from` or `groups` map are allowed; `"*"` acts as wildcard fallback |
| `open` | All groups can interact with the bot |
| `disabled` | All group messages are dropped |

### Groups Map

The `groups` field is a map from ChatID to a rule object. Each rule can optionally specify:

- **`enabled`** — `true` (default if omitted) or `false` to disable a specific group
- **`allow_from`** — sender allowlist within the group; empty means all group members can use the bot

The special key `"*"` serves as a wildcard fallback for any group not explicitly listed.

#### Access Check Flow

```
Group message received
  → group_policy == "disabled"   → drop
  → group_policy == "open"       → allow
  → group_policy == "allowlist"  →
      If group_allow_from is non-empty:
        chatID must be in group_allow_from (or "*")
          → not found → drop
      Lookup groups[chatID], fallback groups["*"]
        → no entry found:
            if group_allow_from matched → allow
            else → drop
        → entry.enabled == false → drop
        → entry.allow_from non-empty && sender not in allow_from → drop
        → allow (strip @mention, dispatch)
```

### Examples

**Allow specific groups, restrict users in one of them:**

```json
{
  "channels": {
    "wecom-bot": {
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

- `wrkSFfCgAAxxxxxx` — only `zhangsan` and `lisi` can trigger the bot
- `wrkSFfCgAAyyyyyy` — all members can trigger the bot
- Any other group — dropped

**Use `group_allow_from` for simple group allowlisting:**

```json
{
  "channels": {
    "wecom-bot": {
      "type": "wecom",
      "group_policy": "allowlist",
      "group_allow_from": ["wrkSFfCgAAxxxxxx", "wrkSFfCgAAyyyyyy"]
    }
  }
}
```

- Only the two listed groups are allowed; no per-group sender filtering

**Combine `group_allow_from` with per-group rules:**

```json
{
  "channels": {
    "wecom-bot": {
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

- `wrkSFfCgAAxxxxxx` — allowed by `group_allow_from`, but only `zhangsan` can trigger (per-group rule)
- `wrkSFfCgAAyyyyyy` — allowed by `group_allow_from`, all members can trigger (no per-group rule)
- Any other group — dropped

**Allow all groups by default, disable one:**

```json
{
  "channels": {
    "wecom-bot": {
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

- `wrkSFfCgAAzzzzzz` — explicitly disabled
- All other groups — allowed via `"*"` wildcard

**Wildcard with sender filter (only admins in any group):**

```json
{
  "channels": {
    "wecom-bot": {
      "type": "wecom",
      "group_policy": "allowlist",
      "groups": {
        "*": { "allow_from": ["admin1", "admin2"] }
      }
    }
  }
}
```

**Open to all groups (no per-group config needed):**

```json
{
  "channels": {
    "wecom-bot": {
      "type": "wecom",
      "group_policy": "open"
    }
  }
}
```

Or via CLI:

```bash
clawdex config set channels.wecom-bot.group_policy open
```

## Per-ChatType Sandbox

By default, the Codex sandbox level (`codex.sandbox`) applies to all messages. You can set a stricter sandbox for group messages using `codex.group_sandbox`:

```json
{
  "codex": {
    "sandbox": "danger-full-access",
    "group_sandbox": "read-only"
  }
}
```

- **DM messages** use `codex.sandbox` (e.g. `danger-full-access`)
- **Group messages** use `codex.group_sandbox` if set, otherwise fall back to `codex.sandbox`

This allows running a permissive sandbox for trusted DM users while restricting group chats to read-only access.

Set via environment variable:

```bash
export CODEX_GROUP_SANDBOX=read-only
```

> **Note:** The `groups` map is loaded from the config file only. Environment variables are not supported for this setting due to its nested structure.

## Message Types

### Inbound (user -> bot)

**Webhook mode** (encrypted XML):

| MsgType | Handling |
|---------|----------|
| `text` | Text content forwarded to Codex |
| `image` | Image downloaded and passed to Codex as `--image` |
| `mixed` | Images downloaded, text extracted from articles, forwarded to Codex |
| `link` | Title, description, and URL extracted and forwarded as text |
| `voice` | Sent as `[voice]` placeholder (not pushed by WeCom callback) |
| `file` | Sent as `[file: name]` placeholder (not pushed by WeCom callback) |
| `location` | Sent as `[location]` placeholder |
| `event` | Ignored |

> **Note:** WeCom group bot callbacks only push `text`, `image`, and `mixed` messages. Other types (voice, video, file, location) are not delivered by WeCom.

**WebSocket mode** (JSON):

| MsgType | Handling |
|---------|----------|
| `text` | Text content forwarded to Codex |
| `image` | Image URL downloaded and passed to Codex |
| `voice` | Transcribed text used if available (AI bot), otherwise `[voice]` |
| `file` | `[file]` placeholder |
| `mixed` | Text and images extracted from `msg_item` array |
| `event` | Ignored |

### Outbound (bot -> user)

**Webhook mode:** Codex output is sent as **markdown** messages via the webhook URL. When file paths are detected in the output:

- **Images** (`.jpg`, `.jpeg`, `.png`): Sent as base64-encoded image messages (max 2MB; larger files fall back to file upload)
- **Other files**: Uploaded via `upload_media` API, then sent as file messages (max 20MB)

**WebSocket mode:** Codex output is sent via `aibot_respond_msg`. Text uses `markdown` or `stream` frames. When local file paths are detected, clawdex uploads workspace artifacts with `aibot_upload_media_init`, `aibot_upload_media_chunk`, and `aibot_upload_media_finish`, then returns them as `file`, `image`, `voice`, or `video` messages. The current limits follow the official AI Bot WebSocket rules: images up to 2 MB, voice up to 2 MB (`.amr`), video up to 10 MB (`.mp4`), and files up to 20 MB.

## Message Chunking

WeCom markdown messages are limited to **4096 UTF-8 bytes**. Long messages are automatically split at natural boundaries:

1. Paragraph boundary (`\n\n`)
2. Line boundary (`\n`)
3. Space boundary
4. Hard byte boundary (preserving UTF-8 character integrity)

| Setting | Description | Default |
|---------|-------------|---------|
| `text_chunk_limit` | Max UTF-8 bytes per chunk (>= 100) | `4096` |

## Streaming

### Webhook Mode

Webhook mode does not support streaming. The bot waits for the full Codex output before sending.

### WebSocket Mode

WebSocket mode supports **streaming replies** via the `stream` message type. When the gateway's streaming mode is enabled (default `partial`), Codex output streams in real-time with a typewriter effect:

1. **First chunk** arrives → bot sends an initial stream frame (`finish: false`)
2. **Subsequent chunks** → bot sends updated frames with accumulated content snapshot (`finish: false`)
3. **Codex completes** → gateway sends a final frame (`finish: true`)

Each stream frame contains the **full accumulated text** (not a delta), which WeCom replaces in the chat UI.

Stream frame format:
```json
{
  "cmd": "aibot_respond_msg",
  "headers": {"req_id": "<callback_req_id>"},
  "body": {
    "msgtype": "stream",
    "stream": {
      "id": "clawdex-stream-1",
      "finish": false,
      "content": "accumulated text so far..."
    }
  }
}
```

The gateway's standard streaming throttle (1s interval) applies to prevent excessive frame sends.

## Encryption

Webhook mode messages from WeCom are encrypted using AES-256-CBC. The driver handles:

- **URL verification** (GET): Verifies SHA1 signature, decrypts echostr, returns plaintext
- **Message decryption** (POST): Verifies signature, decrypts AES-256-CBC with PKCS7 padding

The encryption uses:
- SHA1 signature: `SHA1(sort([token, timestamp, nonce, encrypted]))`
- AES key: `base64decode(EncodingAESKey + "=")` (32 bytes)
- IV: first 16 bytes of the AES key
- Plaintext format: 16 random bytes + 4-byte message length (big-endian) + message + receiveid

WebSocket mode does not use XML encryption — messages are delivered as plain JSON frames over the encrypted WSS connection.

## Multi-Channel Operation

WeCom can run alongside Telegram. Enable both in the config:

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "bot_token": "${TELEGRAM_BOT_TOKEN}",
      "enabled": true
    },
    "wecom-bot": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

Or run WeCom only by disabling Telegram:

```json
{
  "channels": {
    "telegram": {
      "type": "telegram",
      "enabled": false
    },
    "wecom-bot": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket",
      "botid": "your-bot-id",
      "secret": "${WECOM_SECRET}"
    }
  }
}
```

## Bot Commands

Command availability differs between private chats and groups:

| Command | Private chat | Group chat | Description |
|---------|--------------|------------|-------------|
| `/help` | Yes | Yes | Show the commands available in the current chat |
| `/new` | Yes | No | Start a fresh conversation |
| `/sessions` | Yes | No | List recent sessions (up to 10) |
| `/resume <id>` | Yes | No | Switch to an existing session |
| `/cancel` | Yes | Yes | Cancel the running task |
| `/cron help` | Yes | Yes | Show scheduled job commands |
| `/cron list` | Yes | Yes | List scheduled jobs for the current chat |
| `/cron status <id\|index\|name>` | Yes | Yes | Show a scheduled job |
| `/cron stop <id\|index\|name>` | Yes | Yes | Disable a scheduled job |
| `/cron resume <id\|index\|name>` | Yes | Yes | Re-enable a scheduled job |
| `/cron remove <id\|index\|name>` | Yes | Yes | Delete a scheduled job |
| `/cron clear` | Yes | Yes | Delete all scheduled jobs for the current chat |
| `/status` | Yes | Yes | Show current chat context: channel, scope, session, SOUL.md |

In group chats, `/new`, `/sessions`, and `/resume` return
`not available in group chats`. `/help` also hides those commands in group
context.

Commands are recognized from the text content of messages and work the same
way in both webhook and WebSocket modes.

### Session Management

Each chat maintains its own Codex session. Sessions are persisted to `~/.local/share/clawdex/sessions.json`.

- `/new` clears the current session; the next message starts a fresh conversation.
- `/sessions` lists recent sessions. In Telegram, inline keyboard buttons are shown for quick resume. In WeCom, session IDs are displayed as text — copy and paste the ID to resume.
- `/resume <id>` accepts full UUIDs or short prefixes (e.g. `a3f2`).
- Sessions survive gateway restarts.
- Sessions are shared across channels — a session started in Telegram can be resumed in WeCom and vice versa.

> **Note:** In WebSocket mode, WeCom renders command shortcuts with template
> cards. Webhook mode falls back to text-only output; commands still work by
> typing them manually.

## Configuration Reference

Global Codex settings such as `codex.workdir` and `CODEX_WORKDIR` are shared
across channels. If `codex.workdir` is omitted, clawdex automatically creates
and uses `~/.clawdex/workspace`.

### Webhook Mode

| Config Key | Env Variable | Description | Default |
|------------|-------------|-------------|---------|
| `channels.<name>.enabled` | - | Enable WeCom channel | `false` |
| `channels.<name>.connection_mode` | - | `webhook` or `websocket` | `webhook` |
| `channels.<name>.token` | - | Callback verification token (required for webhook) | - |
| `channels.<name>.encoding_aes_key` | - | 43-char AES key (required for webhook) | - |
| `channels.<name>.webhook_path` | - | HTTP endpoint path (required for webhook) | - |
| `channels.<name>.text_chunk_limit` | - | Max UTF-8 bytes per chunk (>= 100) | `4096` |
| `channels.<name>.wecom_dm_policy` | - | `open`, `pairing`, `allowlist` | `pairing` |
| `channels.<name>.wecom_allow_from` | - | Comma-separated UserID strings | - |
| `channels.<name>.group_allow_from` | - | Comma-separated ChatID strings for group-level allowlist | - |
| `channels.<name>.group_policy` | - | `open`, `allowlist`, `disabled` | `allowlist` |
| `channels.<name>.groups` | *(file only)* | Per-group rules map (see [Group Access Control](#group-access-control)) | - |
| `codex.group_sandbox` | `CODEX_GROUP_SANDBOX` | Override sandbox level for group messages | *(falls back to `codex.sandbox`)* |

### WebSocket Mode (additional fields)

| Config Key | Env Variable | Description | Default |
|------------|-------------|-------------|---------|
| `channels.<name>.botid` | - | Bot ID (required for websocket) | - |
| `channels.<name>.secret` | - | WebSocket secret (required for websocket) | - |
| `channels.<name>.ws_url` | - | WebSocket server URL | `wss://openws.work.weixin.qq.com` |
| `channels.<name>.heartbeat_interval` | - | Heartbeat ping interval (duration string) | `30s` |

> **Note:** In WebSocket mode, `token`, `encoding_aes_key`, and `webhook_path` are optional. If provided, they enable dual-mode operation (both webhook and WebSocket).

## ChatID Mapping

WeCom uses string-based ChatIDs (e.g. `wkxxxx`). Internally, clawdex hashes these to int64 via FNV-64a to match the `channel.Message.ChatID` type. The original ChatID is preserved for outbound message routing.

## Webhook URL Caching

In webhook mode, reply URLs are cached per chat:

- **Notification bot**: Each inbound message includes a `WebhookUrl` for replies, cached with a **2-hour TTL**. The URL is reusable across multiple replies.
- **AI bot**: Each inbound message includes a one-shot `response_url`, cached with a **1-hour TTL**. The URL can only be called **once** — subsequent replies to the same chat require a new inbound message. This means long-running Codex tasks may fail to reply if the URL expires.

The cache is refreshed with each incoming message.

In WebSocket mode, no URL caching is needed — replies are sent as WebSocket frames using the callback `req_id`.
