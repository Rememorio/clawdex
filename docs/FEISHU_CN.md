# 飞书渠道

clawdex 飞书渠道的详细参考文档。

## 概述

飞书渠道通过飞书开放平台官方 Go SDK 的长连接接入。驱动通过 WebSocket 接收
`im.message.receive_v1` 事件，并通过 IM v1 消息接口回复。

主要特点：

- **长连接** — 不需要公网回调 URL。
- **官方 API** — 使用 `github.com/larksuite/oapi-sdk-go/v3`。
- **私聊 + 群聊** — 支持单聊和群聊。
- **群聊安全默认值** — 默认要求 @机器人。
- **文本回复** — 当前先支持文本消息；媒体可后续扩展。

## 设置

1. 打开 [飞书开放平台](https://open.feishu.cn)，创建或选择应用。
2. 启用 **机器人** 能力。
3. 在 **凭证与基础信息** 获取 **App ID** 和 **App Secret**。
4. 在 **事件与回调** 中，将订阅方式设为 **长连接**。
5. 添加 **接收消息 v2.0** 事件（`im.message.receive_v1`）。
6. 至少开通一个消息权限：
   - 读取用户发给机器人的单聊消息。
   - 获取群组中用户 @机器人的消息。
   - 获取群组中所有消息。如开通该敏感权限，建议保持 `require_mention: true`。
7. 发布应用修改。

运行 `clawdex onboard`，选择 **Add Feishu instance**。

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

## 配置

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

## 访问控制

### 私聊策略

| 策略 | 行为 |
|------|------|
| `pairing`（默认） | 未知用户收到配对码，管理员通过 CLI 审批 |
| `open` | 处理所有私聊消息 |
| `allowlist` | 仅 `allow_from` 中的用户 `open_id` 可交互 |

飞书配对和白名单使用发送者 `open_id`，例如 `ou_xxx`。

### 群聊策略

| 策略 | 行为 |
|------|------|
| `allowlist`（默认） | 仅 `group_allow_from` 中列出的会话生效 |
| `open` | 机器人所在任意群都可触发 clawdex |
| `disabled` | 忽略所有群消息 |

群白名单使用飞书 `chat_id`，例如 `oc_xxx`。默认 `require_mention` 为
`true`，可避免应用拥有“获取群组中所有消息”敏感权限时回复每条群消息。

可在 JSON 文件中配置单群规则：

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

## 配置参考

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `type` | string | — | 必须为 `"feishu"` |
| `enabled` | bool | `true` | 启用/禁用此实例 |
| `app_id` | string | — | 飞书 App ID，支持 `${ENV_VAR}` 和 `file://` |
| `app_secret` | string | — | 飞书 App Secret，支持 `${ENV_VAR}` 和 `file://` |
| `base_url` | string | `https://open.feishu.cn` | 可选 API 地址，例如 `https://open.larksuite.com` |
| `dm_policy` | string | `"pairing"` | 私聊权限：`open`、`pairing`、`allowlist` |
| `allow_from` | string[] | `[]` | 用户 `open_id` 白名单 |
| `group_policy` | string | `"allowlist"` | 群聊权限：`open`、`allowlist`、`disabled` |
| `group_allow_from` | string[] | `[]` | 群 `chat_id` 白名单 |
| `groups` | object | — | 单群规则 |
| `require_mention` | bool | `true` | 群聊中是否要求 @机器人 |
| `text_chunk_limit` | int | `4000` | 单条文本消息最大字符数 |

## 环境变量

```bash
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your-app-secret"
export FEISHU_DM_POLICY="pairing"
export FEISHU_GROUP_POLICY="allowlist"
export FEISHU_GROUP_ALLOW_FROM="oc_xxx"
```

## 故障排查

- 如果网关日志出现长连接鉴权错误，检查 `app_id` 和 `app_secret`。
- 如果收不到消息，确认 **事件与回调** 使用长连接，并已订阅 `im.message.receive_v1`。
- 如果收不到私聊消息，检查应用对发送者是否可用，以及单聊消息权限是否开通。
- 如果收不到群消息，确认机器人已入群，并开通 @机器人或群组全量消息权限。
- 如果群消息到达但 clawdex 不响应，检查 `group_policy`、`group_allow_from` 和 `require_mention`。
