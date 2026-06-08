# Feishu Channel

Detailed reference for the Feishu channel in clawdex.

## Overview

The Feishu channel connects to Feishu Open Platform via the official Go SDK
long connection. It receives `im.message.receive_v1` events over WebSocket and
uses the IM v1 message API to reply.

Key characteristics:

- **Long connection** — no public callback URL is required.
- **Official API** — uses `github.com/larksuite/oapi-sdk-go/v3`.
- **DM + Group** — supports private chats and group chats.
- **Safe group default** — group messages require @bot by default.
- **Text + inbound images** — handles text, rich text, voice transcripts, and
  image resources that Codex can inspect.
- **Status reactions** — adds a `Typing` reaction while Codex is working and
  replaces it with `THUMBSUP` on completion or `ERROR` on cancellation.

## Bot Setup

1. Open [Feishu Open Platform](https://open.feishu.cn) and create or select an app.
2. Enable the **Bot** capability.
3. In **Credentials & Basic Info**, note the **App ID** and **App Secret**.
4. In **Events & Callbacks**, set the subscription mode to **long connection**.
5. Add the **Receive message v2.0** event (`im.message.receive_v1`).
6. Grant at least one message permission:
   - Read messages users send to the bot in private chats.
   - Read @bot messages in groups.
   - Read all group messages, if you need it. Keep `require_mention: true`.
   - Read/download message resources, if users will send images.
   - Add and delete message reactions, if you want status reactions.
7. Publish the app changes.

Run `clawdex onboard` and select **Add Feishu instance**.

```
$ clawdex onboard
...
  Choice [1/2/3/4/5/6]: 6

Feishu
  Instance name [feishu]:

  App ID source:
    > 1. Environment variable (recommended)
      2. Plaintext
      3. File path

  Choice [1/2/3] [1]: 1
  Environment variable name [FEISHU_APP_ID]:

  App Secret source:
    > 1. Environment variable (recommended)
      2. Plaintext
      3. File path

  Choice [1/2/3] [1]: 1
  Environment variable name [FEISHU_APP_SECRET]:

  ✓ Feishu "feishu" configured (long connection)
  ℹ No callback URL needed — bot connects outbound via WebSocket
```

## Configuration

```json
{
  "channels": {
    "feishu": {
      "type": "feishu",
      "enabled": true,
      "app_id": "${FEISHU_APP_ID}",
      "app_secret": "${FEISHU_APP_SECRET}",
      "dm_policy": "pairing",
      "allow_from": [],
      "group_policy": "allowlist",
      "group_allow_from": [],
      "require_mention": true,
      "text_chunk_limit": 4000
    }
  }
}
```

## Access Control

### DM Policy

| Policy | Behavior |
|--------|----------|
| `pairing` (default) | Unknown users receive a pairing code; admin approves via CLI |
| `open` | All private messages are processed |
| `allowlist` | Only user `open_id` values in `allow_from` can interact |

Feishu pairing and allowlists use the sender `open_id`, for example `ou_xxx`.

### Group Policy

| Policy | Behavior |
|--------|----------|
| `allowlist` (default) | Only chats listed in `group_allow_from` are active |
| `open` | Any group the bot is in can trigger clawdex |
| `disabled` | Ignore all group messages |

Group allowlists use Feishu `chat_id`, for example `oc_xxx`. By default,
`require_mention` is `true`; this prevents the bot from responding to every
group message when the app has the sensitive "read all group messages" permission.

Per-group rules can be configured in the JSON file:

```json
{
  "groups": {
    "oc_xxx": {
      "enabled": true,
      "allow_from": ["ou_user_1"],
      "require_mention": true
    }
  }
}
```

## Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | Must be `"feishu"` |
| `enabled` | bool | `true` | Enable/disable this instance |
| `app_id` | string | — | Feishu App ID, supports `${ENV_VAR}` and `file://` |
| `app_secret` | string | — | Feishu App Secret, supports `${ENV_VAR}` and `file://` |
| `base_url` | string | `https://open.feishu.cn` | Optional API base URL, e.g. `https://open.larksuite.com` |
| `dm_policy` | string | `"pairing"` | DM access: `open`, `pairing`, `allowlist` |
| `allow_from` | string[] | `[]` | User `open_id` allowlist |
| `group_policy` | string | `"allowlist"` | Group access: `open`, `allowlist`, `disabled` |
| `group_allow_from` | string[] | `[]` | Group `chat_id` allowlist |
| `groups` | object | — | Per-group rules |
| `require_mention` | bool | `true` | Require @bot in groups |
| `text_chunk_limit` | int | `4000` | Max characters per outbound text message |

## Environment Variables

```bash
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your-app-secret"
export FEISHU_DM_POLICY="pairing"
export FEISHU_GROUP_POLICY="allowlist"
export FEISHU_GROUP_ALLOW_FROM="oc_xxx"
```

## Status Reactions

For normal Codex runs, clawdex reacts to the source message before the reply is
ready. Feishu renders this as a `Typing` reaction. When the run completes, the
driver deletes the old reaction and adds `THUMBSUP`; cancelled runs use
`ERROR`. If the bot lacks reaction permissions or the message cannot be reacted
to, clawdex logs a warning and continues sending the text reply.

## Image Messages

For `image` messages and images embedded in Feishu rich text (`post`) messages,
clawdex downloads the image resources to a temporary directory and passes those
local image paths to Codex. Non-image attachments are represented as text
placeholders for now.

## Troubleshooting

- If the gateway logs long-connection authentication errors, verify `app_id` and `app_secret`.
- If no messages arrive, confirm **Events & Callbacks** uses long connection and includes `im.message.receive_v1`.
- If private messages do not arrive, check that the app is available to the sender and has private message permission.
- If group messages do not arrive, add the bot to the group and grant @bot or all-group-message permission.
- If group messages arrive but clawdex does not respond, check `group_policy`, `group_allow_from`, and `require_mention`.
- If status reactions do not appear, grant the app message reaction permissions
  and republish the app.
- If image messages are received as placeholders only, grant the app message
  resource permissions and republish it.
