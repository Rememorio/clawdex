# Telegram Channel

Detailed reference for the Telegram channel in clawdex.

## Bot Setup

1. Create a bot via [BotFather](https://t.me/BotFather) (`/newbot`).
2. Copy the bot token.
3. Run `clawdex onboard` and paste the token when prompted.

The token is saved to `~/.clawdex/clawdex.json` and supports three formats:

| Format | Example | Description |
|--------|---------|-------------|
| Plain string | `123456:ABC-DEF` | Stored directly |
| Env var ref | `${TELEGRAM_BOT_TOKEN}` | Resolved from environment at runtime |
| File ref | `file:///run/secrets/token` | Read from file at runtime |

### Recommended: Environment Variable

The `clawdex onboard` wizard defaults to an environment variable reference. Set it before starting the gateway:

```bash
export TELEGRAM_BOT_TOKEN="123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
```

The config file will then contain `${TELEGRAM_BOT_TOKEN}`, which is resolved at runtime. This avoids storing the token in plaintext on disk.

## Access Control

Controlled by `dm_policy` (config file) or `TELEGRAM_DM_POLICY` (env).

| Policy | Behavior |
|--------|----------|
| `pairing` (default) | Unknown users receive a pairing code; admin approves via CLI |
| `allowlist` | Only user IDs listed in `allow_from` can interact |
| `open` | Anyone can DM the bot |

### Pairing Flow

1. User sends any message to the bot.
2. Bot replies with a 6-character pairing code (e.g. `A3X9K2`).
3. Admin runs:
   ```bash
   clawdex pairing approve A3X9K2
   ```
4. The user ID is added to the runtime allow list **and** persisted to
   `allow_from` in the config file. The change takes effect immediately
   — no gateway restart is needed.
5. Bot notifies the user: "You have been approved!"

Management commands:

```bash
clawdex pairing list              # List pending pairing requests
clawdex pairing approve <CODE>    # Approve a request
```

### Allowlist

Set `dm_policy` to `"allowlist"` and list user IDs:

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

The first Telegram instance created by `clawdex onboard` is named
`telegram` by default. If you kept that name, you can use:

```bash
clawdex config set channels.telegram.dm_policy allowlist
clawdex config set channels.telegram.allow_from 123456789,987654321
```

Users can find their user ID from the gateway logs. After sending the bot a
DM, look for a `telegram recv` log line and read the `sender_id` field.

## Group Support

Clawdex can respond in Telegram groups and supergroups. Access is controlled
by `group_policy`:

| Policy | Behavior |
|--------|----------|
| `allowlist` (default) | Only whitelisted groups can use the bot |
| `open` | Bot responds in all groups |
| `disabled` | Bot ignores all group messages |

### Group Configuration

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

Common CLI commands with the default instance name are:

```bash
clawdex config set channels.telegram.group_policy allowlist
clawdex config set channels.telegram.group_allow_from -1001234567890
clawdex config set channels.telegram.require_mention true
```

> `groups` is a structured map and is best edited directly in
> `~/.clawdex/clawdex.json`.

### Group Access Layers

1. **Global whitelist** (`group_allow_from`): List of group chat IDs allowed
   to interact with the bot.
2. **Per-group rules** (`groups` map): Fine-grained control per group:
   - `enabled`: Enable or disable a specific group.
   - `allow_from`: User IDs allowed within this group.
   - `require_mention`: Override the global mention requirement.

### @ Mention Requirement

By default, the bot only responds when **@mentioned** in groups. This keeps
it from replying to every message in busy chats.

- Global setting: `require_mention: true` (default).
- Per-group override: `groups.<chat_id>.require_mention: false`.

When `require_mention` is enabled, both normal messages and slash commands in
that group must mention the bot before the gateway will process them.

### Getting Group Chat ID

To find a group's chat ID:

1. Add the bot to the target group.
2. Send any message in the group.
3. Check the gateway output for a `telegram recv` or `telegram skip` log line.
4. Copy the `chat_id` field from that line. Supergroups usually look like
   `-100xxxxxxxxxx`.

Even if the message is currently blocked by `group_policy`,
`group_allow_from`, or `require_mention`, the log line still includes the
same `chat_id`.

### Environment Variables

| Env Variable | Description |
|--------------|-------------|
| `TELEGRAM_GROUP_POLICY` | `open`, `allowlist`, `disabled` |
| `TELEGRAM_GROUP_ALLOW_FROM` | Comma-separated group chat IDs |

## Streaming

Controlled by `streaming` (config) or `TELEGRAM_STREAMING` (env).

| Mode | Behavior |
|------|----------|
| `partial` (default) | Bot sends an initial message and edits it as output streams in |
| `progress` | Same as `partial` (alias) |
| `off` | Bot waits for full output, then sends a single message |

## Message Chunking

Long messages are split before sending. Controlled by `chunk_mode` and `text_chunk_limit`.

| Setting | Description | Default |
|---------|-------------|---------|
| `chunk_mode` | `"length"` (hard rune split) or `"newline"` (split on paragraph boundaries) | `length` |
| `text_chunk_limit` | Max runes per chunk (>= 100) | `3500` |

In `newline` mode, the splitter tries `\n\n` boundaries first, then `\n`, then spaces, then hard rune split as a last resort.

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

Commands are registered in Telegram's command menu automatically on startup.
If `require_mention` is enabled for the current group, slash commands must
mention the bot too.

## Session Management

Each chat maintains its own session. Sessions are persisted to `~/.local/share/clawdex/sessions.json`.

- `/new` clears the current session; the next message starts fresh.
- `/sessions` lists recent sessions with inline keyboard buttons for quick resume.
- `/resume <id>` accepts full UUIDs or short prefixes (e.g. `a3f2`).
- Sessions survive gateway restarts.

## Media

### Inbound (user -> bot)

The bot downloads and forwards these media types to Codex as `--image` arguments:

- Photos (largest available size)
- Static stickers (WEBP)

Other media types (video, audio, voice, documents, animated/video stickers) are represented as text placeholders (e.g. `[video]`, `[audio]`, `[document: file.pdf]`).

### Outbound (bot -> user)

When Codex output contains file paths, the gateway detects and sends them as attachments:

- Images: `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`
- Voice: `.ogg`, `.oga`
- Documents: everything else (`.pdf`, `.txt`, `.zip`, etc.)

Captions are limited to 1024 characters (Telegram limit); overflow is sent as a separate text message.

## Formatting

Codex output (Markdown) is converted to Telegram HTML before sending:

- `**bold**` -> `<b>bold</b>`
- `*italic*` / `_italic_` -> `<i>italic</i>`
- `` `code` `` -> `<code>code</code>`
- Code blocks -> `<pre><code class="language-x">...</code></pre>`
- `~~strike~~` -> `<s>strike</s>`
- `> quote` -> expandable blockquote
- `[text](url)` -> `<a href="url">text</a>`
- Thinking tags (`<thinking>...</thinking>`) -> expandable blockquote

Raw HTML in Codex output is escaped to prevent Telegram parse errors. If HTML parsing still fails, the message is retried as plain text.

Link previews are enabled by default. When the bot sends a message containing a URL, Telegram will show a rich preview below the message.

## SOUL.md

You can inject a system prompt into every Codex session by creating `~/.clawdex/SOUL.md`:

```bash
cat > ~/.clawdex/SOUL.md << 'EOF'
You are a helpful coding assistant.
EOF
```

The file content is passed to Codex as `--instructions` on every invocation. No config change is needed — the gateway loads `~/.clawdex/SOUL.md` automatically if it exists.

## Health Check

The gateway exposes an HTTP server (default `:8080`) with a `/healthz` endpoint:

```bash
curl -sS http://127.0.0.1:8080/healthz
```

## Configuration Reference

All settings can be set in `~/.clawdex/clawdex.json` or overridden by environment variables.

### Telegram Settings

> `clawdex config` uses `channels.<instance>.<field>` keys. The first
> Telegram instance is usually named `telegram`.

| Config Key | Env Variable | Description | Default |
|------------|-------------|-------------|---------|
| `channels.<name>.bot_token` | `TELEGRAM_BOT_TOKEN` | Bot token (required) | - |
| `channels.<name>.enabled` | `TELEGRAM_ENABLED` | Enable/disable Telegram | `true` |
| `channels.<name>.dm_policy` | `TELEGRAM_DM_POLICY` | `open`, `pairing`, `allowlist` | `pairing` |
| `channels.<name>.allow_from` | `TELEGRAM_ALLOW_FROM` | Comma-separated user IDs | - |
| `channels.<name>.group_policy` | `TELEGRAM_GROUP_POLICY` | `open`, `allowlist`, `disabled` | `allowlist` |
| `channels.<name>.group_allow_from` | `TELEGRAM_GROUP_ALLOW_FROM` | Comma-separated group chat IDs | - |
| `channels.<name>.require_mention` | - | Require @mention in groups | `true` |
| `channels.<name>.streaming` | `TELEGRAM_STREAMING` | `off`, `partial`, `progress` | `partial` |
| `channels.<name>.chunk_mode` | `TELEGRAM_CHUNK_MODE` | `length`, `newline` | `length` |
| `channels.<name>.text_chunk_limit` | `TELEGRAM_TEXT_CHUNK_LIMIT` | Max runes per chunk (>= 100) | `3500` |
| `channels.<name>.groups` | *file only* | Per-group rule map | - |
| `channels.<name>.poll_timeout` | `TELEGRAM_POLL_TIMEOUT` | Long-poll seconds (1-50) | `30` |
| `channels.<name>.startup_probe_timeout` | `TELEGRAM_STARTUP_PROBE_TIMEOUT` | Startup probe timeout | `8s` |

### Codex Settings

| Config Key | Env Variable | Description | Default |
|------------|-------------|-------------|---------|
| `codex.workdir` | `CODEX_WORKDIR` | Working directory for Codex | `~/.clawdex/workspace` |
| `codex.timeout` | `CODEX_TIMEOUT` | Timeout per request (duration string) | `120m` |
| `codex.sandbox` | `CODEX_SANDBOX` | Sandbox level | `workspace-write` |

### Server Settings

| Config Key | Env Variable | Description | Default |
|------------|-------------|-------------|---------|
| `gateway.address` | `GATEWAY_ADDR` | HTTP listen address | `:8080` |

### Example Config

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

You can also use the `clawdex config` command for non-interactive management:

```bash
clawdex config list                                  # Show all config values
clawdex config get channels.telegram.dm_policy       # Get a specific value
clawdex config set channels.telegram.dm_policy allowlist
clawdex config set channels.telegram.group_policy allowlist
clawdex config set channels.telegram.group_allow_from -1001234567890
clawdex config file                                  # Show config file path
```
