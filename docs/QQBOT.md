# QQ Bot Channel

Detailed reference for the QQ Bot channel in clawdex.

## Overview

The QQ Bot channel connects to QQ via the official QQ Bot Open Platform API.
It maintains a persistent WebSocket connection to the QQ Gateway for receiving
messages, and uses the REST API for sending replies and media.

Key characteristics:

- **Official API** — uses the QQ Bot developer platform (q.qq.com)
- **WebSocket transport** — persistent connection with heartbeat and auto-resume
- **C2C + Group** — supports both private DMs and group @-mention conversations
- **Auto-reconnect** — exponential backoff with session resume on disconnect
- **Media support** — images, audio, video, and files via the rich media API
- **Passive reply limiting** — automatically switches to proactive mode after 4 replies

## Bot Setup

1. Create a bot application at [QQ Bot Platform](https://q.qq.com).
2. Note your **App ID** and **Client Secret** from the developer console.
3. Enable the bot's messaging intents (C2C messages, group @-messages).
4. Run `clawdex onboard` and select **Add QQ Bot instance**.

```
$ clawdex onboard
...
  Choice [1/2/3/4/5]: 4

QQ Bot
  Instance name [qqbot]:

  Get your AppID and ClientSecret from https://q.qq.com

  App ID: 123456789
  Client Secret: ********************************

  ✓ QQ Bot "qqbot" configured
  ℹ DM policy set to open, group policy set to open
```

After onboarding, the config file contains:

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

## Access Control

### DM Policy

Controlled by `dm_policy` in the channel config.

| Policy | Behavior |
|--------|----------|
| `open` (default) | All C2C messages are processed |
| `pairing` | Unknown users receive a pairing code; admin approves via CLI |
| `allowlist` | Only user openids listed in `allow_from` can interact |

### Group Policy

Controlled by `group_policy` in the channel config.

| Policy | Behavior |
|--------|----------|
| `open` | Respond to @-mentions in any group the bot has joined |
| `allowlist` (default) | Only groups listed in `group_allow_from` are active |
| `disabled` | Ignore all group messages |

Group messages require the bot to be @-mentioned (`GROUP_AT_MESSAGE_CREATE`).
The `@BotName` prefix is automatically stripped before passing content to Codex.

## Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | Must be `"qqbot"` |
| `enabled` | bool | `true` | Enable/disable this instance |
| `app_id` | string | — | QQ Bot App ID (supports `${ENV_VAR}`) |
| `client_secret` | string | — | Client secret (supports `${ENV_VAR}`) |
| `dm_policy` | string | `"open"` | DM access control: `open`, `pairing`, `allowlist` |
| `allow_from` | string[] | `[]` | User openid allowlist (DM policy) |
| `group_policy` | string | `"allowlist"` | Group access: `open`, `allowlist`, `disabled` |
| `group_allow_from` | string[] | `[]` | Group openid allowlist |
| `text_chunk_limit` | int | `5000` | Max characters per outbound message |

## Environment Variables

Credentials can be provided via environment variables instead of plaintext:

```bash
export QQ_APP_ID="123456789"
export QQ_CLIENT_SECRET="your-client-secret-here"
```

Reference them in the config with `${QQ_APP_ID}` and `${QQ_CLIENT_SECRET}`.

## Protocol Details

### Connection Lifecycle

1. Obtain access token via `POST https://bots.qq.com/app/getAppAccessToken`
2. Fetch gateway URL via `GET https://api.sgroup.qq.com/gateway`
3. Connect WebSocket, receive Hello (op:10) with heartbeat interval
4. Send Identify (op:2) with token and intents — or Resume (op:6) if session exists
5. Receive READY event with session ID
6. Enter event loop: heartbeat + dispatch handling

### Auto-Reconnect

On disconnect, the driver retries with exponential backoff (3s → 60s cap).
If the server returns an Invalid Session (op:9), the session state is cleared
and a fresh Identify is sent on the next connection.

### Passive Reply Limiting

QQ Bot restricts passive replies to 4 per inbound message per hour. The driver
tracks reply counts per trigger message and automatically drops `msg_id` (switching
to proactive mode) once the limit is reached. This is transparent to Codex.

### Token Refresh

Access tokens are cached and refreshed 60 seconds before expiry. If a request
returns 401, the driver clears the token cache and retries once with a fresh token.

## Media

### Inbound (user → bot)

| Type | Handling |
|------|----------|
| Text | Passed to Codex |
| Image | Downloaded to temp file, passed as `--image` |
| Audio | Downloaded to temp file |
| Video | Downloaded to temp file |
| File | Downloaded to temp file |

Attachments are cleaned up after the Codex session completes.

### Outbound (bot → user)

When Codex output contains file paths, the gateway uploads them via the QQ
rich media API (`/v2/users/{id}/files` or `/v2/groups/{id}/files`):

- Images (`.jpg`, `.png`, `.gif`, `.webp`) — file_type 1
- Video (`.mp4`, `.mov`, `.avi`) — file_type 2
- Audio (`.mp3`, `.wav`, `.silk`) — file_type 3
- Other files — file_type 4

## Bot Commands

All standard clawdex commands are available:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/new` | Start a fresh conversation |
| `/sessions` | List recent sessions |
| `/resume <id>` | Switch to an existing session |
| `/cancel` | Cancel the running task |
| `/status` | Show current chat context |

## SOUL.md

Per-instance SOUL prompts are supported. Create
`~/.clawdex/SOUL-<instance-name>.md` for a QQ-Bot-specific prompt:

```bash
cat > ~/.clawdex/SOUL-qqbot.md << 'EOF'
You are a helpful coding assistant.
EOF
```

Falls back to `~/.clawdex/SOUL.md` if no instance-specific file exists.

## Troubleshooting

### "灵魂不在线" (bot shows offline)

The bot appears offline when no WebSocket connection is active.

- Verify the gateway is running: `clawdex gateway status`
- Check logs: `clawdex gateway run` (foreground mode)
- Ensure App ID and Client Secret are correct
- Confirm the bot's intents are enabled at q.qq.com

### Messages not arriving

- Check `group_policy` — if set to `allowlist`, the group openid must be in `group_allow_from`
- The bot must be @-mentioned in groups (`GROUP_AT_MESSAGE_CREATE`)
- Check `dm_policy` — if set to `allowlist`, the sender's openid must be in `allow_from`

### Token errors in logs

- Verify `client_secret` is correct and not expired
- The driver auto-retries on 401; persistent failures indicate invalid credentials
- Regenerate the secret at q.qq.com if needed

### Gateway crashes on startup

- Ensure both `app_id` and `client_secret` are non-empty
- If using `${ENV_VAR}` syntax, verify the variables are exported
