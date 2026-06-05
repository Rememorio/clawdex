# 微信（Weixin）渠道

clawdex 微信个人号渠道的详细参考文档。

## 概述

微信渠道通过 iLink Bot API 将个人微信消息桥接到 Codex。使用 HTTP 长轮询
（`getUpdates`），无需公网服务器或 Webhook。

主要特点：

- **扫码登录** — 在 `clawdex onboard` 过程中直接扫码绑定
- **长轮询传输** — 无需 Webhook，NAT/防火墙后也能正常工作
- **一对一绑定** — 一个微信号绑定一个 bot 实例
- **输入状态提示** — AI 生成时显示"对方正在输入"
- **CDN 媒体** — 图片、语音、视频、文件的加密上传/下载

## 设置

1. 运行 `clawdex onboard`，选择 **Add Weixin instance**。
2. 终端打印二维码链接。
3. 在浏览器中打开链接，或用微信扫描。
4. 手机上确认连接。
5. Token 自动保存 — 无需手动输入。

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

完成后配置文件内容：

```json
{
  "channels": {
    "weixin": {
      "type": "weixin",
      "enabled": true,
      "base_url": "https://ilinkai.weixin.qq.com",
      "token": "<扫码获取>",
      "dm_policy": "open",
      "allow_from": ["your-id@im.wechat"],
      "text_chunk_limit": 4000
    }
  }
}
```

## 访问控制

通过 `dm_policy` 配置项控制。

| 策略 | 行为 |
|------|------|
| `open`（默认） | 处理所有消息 — 适合一对一扫码绑定的场景 |
| `pairing` | 未知用户收到配对码，管理员通过 CLI 审批 |
| `allowlist` | 仅 `allow_from` 中列出的用户 ID 可以交互 |

默认为 `open`，因为微信个人号通过扫码绑定 — 只有扫码的人才能给 bot 发消息。
扫码者的用户 ID 在 onboard 时自动写入 `allow_from`。

### 配对流程（可选）

如果将 `dm_policy` 设为 `"pairing"`：

1. 用户发送消息。
2. Bot 回复 6 位配对码。
3. 管理员执行 `clawdex pairing approve <CODE>`。
4. 用户 ID 被持久化到配置文件的 `allow_from` 中。

## 配置参考

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `type` | string | — | 必须为 `"weixin"` |
| `enabled` | bool | `true` | 启用/禁用此实例 |
| `base_url` | string | `https://oai.ilink.bot` | API 地址（扫码时自动设置） |
| `token` | string | — | Bot Token（扫码获取） |
| `dm_policy` | string | `"open"` | 访问控制：`open`、`pairing`、`allowlist` |
| `allow_from` | string[] | `[]` | 用户 ID 白名单（xxx@im.wechat） |
| `text_chunk_limit` | int | `4000` | 每条消息最大字符数 |

## 输入状态提示

AI 生成回复期间，driver 每 5 秒发送一次输入状态提示（"对方正在输入"），
回复送达后立即取消。

- 需要 `typing_ticket`（启动时从 `getConfig` 获取）
- 如果获取失败，静默降级（不影响消息收发）

## Markdown 渲染

微信在 AI 智能体对话场景中原生支持 Markdown 渲染（标题、列表、代码块、粗体、
斜体、引用、表格等）。Driver 直接透传 Markdown，仅去除微信无法处理的格式：

| 格式 | 处理 |
|------|------|
| `![alt](url)`（图片语法） | 移除（微信会显示原始文本而非图片） |
| `<thinking>...</thinking>` | 移除（AI 内部推理标签） |
| 其他所有 Markdown | 原样保留，由微信原生渲染 |

## 媒体

### 接收（用户 → bot）

| 类型 | 处理方式 |
|------|----------|
| 文本 | 传递给 Codex |
| 图片 | 从 CDN 下载（AES-128-ECB 解密），作为 `--image` 传入 |
| 语音 | 提取转文字内容（如有） |
| 文件 | 记录日志，跳过 |
| 视频 | 记录日志，跳过 |

### 发送（bot → 用户）

Codex 输出中的文件路径会被检测并上传：

- 图片（`.jpg`、`.png`、`.gif`、`.webp`）
- 视频（`.mp4`、`.avi`、`.mov`）
- 语音（`.amr`、`.mp3`、`.wav`）
- 其他文件

上传流程：读取文件 → AES-128-ECB 加密 → 获取 CDN 预签名 URL → 上传密文 → 通过 `sendMessage` 发送媒体引用。

上传失败时，用户会收到提示：`⚠️ 媒体文件上传失败，请稍后重试。`

## 机器人命令

支持所有 clawdex 标准命令：

| 命令 | 说明 |
|------|------|
| `/help` | 显示可用命令 |
| `/new` | 开始新对话 |
| `/sessions` | 列出最近会话 |
| `/resume <id>` | 切换到已有会话 |
| `/status` | 显示当前聊天上下文 |

## SOUL.md

支持实例级 SOUL 提示词。创建 `~/.clawdex/SOUL-<实例名>.md`：

```bash
cat > ~/.clawdex/SOUL-weixin.md << 'EOF'
你是一个友好的中文助手。
EOF
```

如果没有实例专属文件，回退到 `~/.clawdex/SOUL.md`。

## 故障排查

### Gateway 启动但收不到消息

- 检查 token 是否为空：`clawdex config get channels.weixin.token`
- 如果为空，重新运行 `clawdex onboard` → 添加微信实例 → 扫码
- 查看日志：`clawdex gateway logs -f`

### Session 过期

长时间不活跃后服务端可能过期 session。driver 会记录
`weixin session expired` 并退出。重启 gateway 即可重新连接：

```bash
clawdex gateway restart
```

如果频繁过期，通过 onboard 重新扫码。

### 没有输入状态提示

- 需要从服务端获取 `typing_ticket`。
- 检查 debug 日志中是否有 `weixin getConfig failed`。
- Ticket 在启动时获取一次，重启 gateway 可重试。
