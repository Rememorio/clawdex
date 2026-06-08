package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/Rememorio/clawdex/internal/logger"
)

const maxResourceDownloadBytes = 20 * 1024 * 1024

type sdkLogger struct {
	channel string
}

func (l sdkLogger) Debug(_ context.Context, args ...interface{}) {
	logger.Debug("feishu sdk", "channel", l.channel, "msg", fmt.Sprint(args...))
}

func (l sdkLogger) Info(_ context.Context, args ...interface{}) {
	logger.Info("feishu sdk", "channel", l.channel, "msg", fmt.Sprint(args...))
}

func (l sdkLogger) Warn(_ context.Context, args ...interface{}) {
	logger.Warn("feishu sdk", "channel", l.channel, "msg", fmt.Sprint(args...))
}

func (l sdkLogger) Error(_ context.Context, args ...interface{}) {
	logger.Error("feishu sdk", "channel", l.channel, "msg", fmt.Sprint(args...))
}

type larkMessageAPI struct {
	client    *lark.Client
	mu        sync.Mutex
	botOpenID string
}

func newLarkClient(appID, appSecret, baseURL, name string) *lark.Client {
	opts := []lark.ClientOptionFunc{
		lark.WithLogLevel(larkcore.LogLevelWarn),
		lark.WithLogger(sdkLogger{channel: name}),
	}
	if strings.TrimSpace(baseURL) != "" {
		opts = append(opts, lark.WithOpenBaseUrl(baseURL))
	}
	return lark.NewClient(appID, appSecret, opts...)
}

func newMessageAPI(appID, appSecret, baseURL, name string) messageAPI {
	return &larkMessageAPI{client: newLarkClient(appID, appSecret, baseURL, name)}
}

func (a *larkMessageAPI) BotOpenID(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.botOpenID != "" {
		return a.botOpenID, nil
	}

	resp, err := a.client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("feishu bot info: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("feishu bot info: empty response")
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("feishu bot info: status=%d", resp.StatusCode)
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return "", fmt.Errorf("parse feishu bot info: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("feishu bot info: code=%d msg=%s", result.Code, result.Msg)
	}
	if result.Bot.OpenID == "" {
		return "", fmt.Errorf("feishu bot info: missing open_id")
	}
	a.botOpenID = result.Bot.OpenID
	return a.botOpenID, nil
}

func (a *larkMessageAPI) ReplyText(ctx context.Context, messageID, text string, replyInThread bool) error {
	content, err := marshalTextContent(text)
	if err != nil {
		return err
	}

	resp, err := a.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(content).
			ReplyInThread(replyInThread).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu reply message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reply message: code=%d msg=%s log_id=%s", resp.Code, resp.Msg, resp.RequestId())
	}
	return nil
}

func (a *larkMessageAPI) SendText(ctx context.Context, chatID, text string) error {
	content, err := marshalTextContent(text)
	if err != nil {
		return err
	}

	resp, err := a.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(content).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu send message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send message: code=%d msg=%s log_id=%s", resp.Code, resp.Msg, resp.RequestId())
	}
	return nil
}

func (a *larkMessageAPI) CreateReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	messageID = strings.TrimSpace(messageID)
	emojiType = strings.TrimSpace(emojiType)
	if messageID == "" {
		return "", fmt.Errorf("feishu create reaction: missing message_id")
	}
	if emojiType == "" {
		return "", fmt.Errorf("feishu create reaction: missing emoji_type")
	}

	resp, err := a.client.Im.MessageReaction.Create(ctx, larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().
				EmojiType(emojiType).
				Build()).
			Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu create reaction: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu create reaction: code=%d msg=%s log_id=%s", resp.Code, resp.Msg, resp.RequestId())
	}
	if resp.Data == nil || resp.Data.ReactionId == nil || strings.TrimSpace(*resp.Data.ReactionId) == "" {
		return "", fmt.Errorf("feishu create reaction: missing reaction_id")
	}
	return strings.TrimSpace(*resp.Data.ReactionId), nil
}

func (a *larkMessageAPI) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	messageID = strings.TrimSpace(messageID)
	reactionID = strings.TrimSpace(reactionID)
	if messageID == "" {
		return fmt.Errorf("feishu delete reaction: missing message_id")
	}
	if reactionID == "" {
		return nil
	}

	resp, err := a.client.Im.MessageReaction.Delete(ctx, larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build())
	if err != nil {
		return fmt.Errorf("feishu delete reaction: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu delete reaction: code=%d msg=%s log_id=%s", resp.Code, resp.Msg, resp.RequestId())
	}
	return nil
}

func (a *larkMessageAPI) DownloadResource(ctx context.Context, messageID, fileKey, resourceType, destPath string) error {
	messageID = strings.TrimSpace(messageID)
	fileKey = strings.TrimSpace(fileKey)
	resourceType = strings.TrimSpace(resourceType)
	destPath = strings.TrimSpace(destPath)
	if messageID == "" {
		return fmt.Errorf("feishu download resource: missing message_id")
	}
	if fileKey == "" {
		return fmt.Errorf("feishu download resource: missing file_key")
	}
	if resourceType == "" {
		return fmt.Errorf("feishu download resource: missing type")
	}
	if destPath == "" {
		return fmt.Errorf("feishu download resource: missing destination")
	}

	resp, err := a.client.Im.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(resourceType).
		Build())
	if err != nil {
		return fmt.Errorf("feishu download resource: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu download resource: code=%d msg=%s log_id=%s", resp.Code, resp.Msg, resp.RequestId())
	}
	if resp.File == nil {
		return fmt.Errorf("feishu download resource: empty file")
	}

	data, err := io.ReadAll(io.LimitReader(resp.File, maxResourceDownloadBytes+1))
	if err != nil {
		return fmt.Errorf("read feishu resource: %w", err)
	}
	if len(data) > maxResourceDownloadBytes {
		return fmt.Errorf("feishu resource exceeds %d bytes", maxResourceDownloadBytes)
	}
	if err := os.WriteFile(destPath, data, 0o600); err != nil {
		return fmt.Errorf("write feishu resource: %w", err)
	}
	return nil
}

func marshalTextContent(text string) (string, error) {
	data, err := json.Marshal(textContent{Text: text})
	if err != nil {
		return "", fmt.Errorf("marshal feishu text content: %w", err)
	}
	return string(data), nil
}
