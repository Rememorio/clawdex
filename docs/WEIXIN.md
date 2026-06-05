# Weixin (微信) Channel

Detailed reference for the Weixin personal WeChat channel in clawdex.

## Overview

The Weixin channel bridges personal WeChat messages to Codex via the iLink bot
API. It uses HTTP long-polling (`getUpdates`) and requires no public-facing
server or webhook URL.

Key characteristics:

- **QR code login** — scan during `clawdex onboard` to bind your WeChat
- **Long-poll transport** — no webhook needed, works behind NAT/firewalls
- **1:1 binding** — one WeChat account binds to one bot instance
- **Typing indicators** — "对方正在输入" displayed while AI generates
- **CDN media** — encrypted upload/download for images, voice, video, files

## Bot Setup

1. Run `clawdex onboard` and select **Add Weixin instance**.
2. A QR code URL is printed in the terminal.
3. Open the URL in a browser or scan with WeChat.
4. Confirm the connection on your phone.
5. The token is saved automatically — no manual entry needed.

```
$ clawdex onboard
...
  Choice [1/2/3/4]: 3

Weixin (微信)
  Instance name [weixin]:

  Scan the QR code with your WeChat app to connect

  请用手机微信扫描以下链接中的二维码：

  https://liteapp.weixin.qq.com/q/...

  等待扫码...
  ✓ Weixin "weixin" connected
```

After onboarding, the config file contains:

```json
{
  "channels": {
    "weixin": {
      "type": "weixin",
      "enabled": true,
      "base_url": "https://ilinkai.weixin.qq.com",
      "token": "<obtained-via-qr-scan>",
      "dm_policy": "open",
      "allow_from": ["your-id@im.wechat"],
      "text_chunk_limit": 4000
    }
  }
}
```

## Access Control

Controlled by `dm_policy` in the channel config.

| Policy | Behavior |
|--------|----------|
| `open` (default) | All messages are processed — appropriate for 1:1 QR-bound bots |
| `pairing` | Unknown users receive a pairing code; admin approves via CLI |
| `allowlist` | Only user IDs listed in `allow_from` can interact |

The default is `open` because WeChat personal bots are bound via QR scan —
only the person who scanned can send messages to the bot. The scanner's user
ID is automatically added to `allow_from` during onboarding.

### Pairing Flow (optional)

If you set `dm_policy` to `"pairing"`:

1. User sends a message.
2. Bot replies with a 6-character code.
3. Admin runs `clawdex pairing approve <CODE>`.
4. The user ID is persisted to `allow_from` in the config file.

## Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | Must be `"weixin"` |
| `enabled` | bool | `true` | Enable/disable this instance |
| `base_url` | string | `https://oai.ilink.bot` | API base URL (set during QR login) |
| `token` | string | — | Bot token (obtained via QR scan) |
| `dm_policy` | string | `"open"` | Access control: `open`, `pairing`, `allowlist` |
| `allow_from` | string[] | `[]` | User ID allowlist (xxx@im.wechat) |
| `text_chunk_limit` | int | `4000` | Max runes per outbound message chunk |

## Typing Indicators

The driver sends typing indicators ("对方正在输入") while AI is generating a
response. The indicator is refreshed every 5 seconds via a keepalive
goroutine and cancelled when the reply is delivered.

Requirements:
- The `typing_ticket` is obtained from `getConfig` at startup.
- If the ticket is unavailable, typing indicators degrade gracefully (no-op).

## Markdown Rendering

WeChat natively renders Markdown in AI bot conversations (headings, lists,
code blocks, bold, italic, blockquotes, tables). The driver passes Markdown
through as-is, only stripping constructs that WeChat cannot handle:

| Construct | Action |
|-----------|--------|
| `![alt](url)` (image syntax) | Removed (WeChat shows raw text, not images) |
| `<thinking>...</thinking>` | Removed (internal AI reasoning tags) |
| Everything else | Passed through for native rendering |

## Media

### Inbound (user -> bot)

| Type | Handling |
|------|----------|
| Text | Passed to Codex |
| Image | Downloaded from CDN (AES-128-ECB decrypted), passed as `--image` |
| Voice | Transcription text extracted (if available) |
| File | Logged, skipped |
| Video | Logged, skipped |

### Outbound (bot -> user)

When Codex output contains file paths, the gateway detects and uploads them:

- Images (`.jpg`, `.png`, `.gif`, `.webp`) — uploaded as image type
- Video (`.mp4`, `.avi`, `.mov`) — uploaded as video type
- Voice (`.amr`, `.mp3`, `.wav`) — uploaded as voice type
- Other files — uploaded as file type

Upload pipeline: read file → AES-128-ECB encrypt → get pre-signed CDN URL →
upload ciphertext → send media reference via `sendMessage`.

If upload fails, the user receives an error notice:
`⚠️ 媒体文件上传失败，请稍后重试。`

## Bot Commands

All standard clawdex commands are available:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/new` | Start a fresh conversation |
| `/sessions` | List recent sessions |
| `/resume <id>` | Switch to an existing session |
| `/status` | Show current chat context |

## SOUL.md

Per-instance SOUL prompts are supported. Create
`~/.clawdex/SOUL-<instance-name>.md` for a Weixin-specific prompt:

```bash
cat > ~/.clawdex/SOUL-weixin.md << 'EOF'
你是一个友好的中文助手。
EOF
```

Falls back to `~/.clawdex/SOUL.md` if no instance-specific file exists.

## Troubleshooting

### Gateway starts but no messages arrive

- Check that the token is not empty: `clawdex config get channels.weixin.token`
- If empty, re-run `clawdex onboard` → Add Weixin instance → scan QR
- Check logs: `clawdex gateway logs -f`

### Session expired

The server may expire the session after extended inactivity. The driver logs
`weixin session expired` and exits. Restart the gateway to reconnect:

```bash
clawdex gateway restart
```

If sessions expire frequently, re-scan the QR code via onboard.

### No typing indicator

- Typing requires a valid `typing_ticket` from the server.
- Check debug logs for `weixin getConfig failed`.
- The ticket is fetched once at startup; restart gateway to retry.
