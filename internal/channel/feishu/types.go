package feishu

import "context"

type textContent struct {
	Text string `json:"text"`
}

type messageAPI interface {
	ReplyText(ctx context.Context, messageID, text string, replyInThread bool) error
	SendText(ctx context.Context, chatID, text string) error
	CreateReaction(ctx context.Context, messageID, emojiType string) (string, error)
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	DownloadResource(ctx context.Context, messageID, fileKey, resourceType, destPath string) error
	BotOpenID(ctx context.Context) (string, error)
}
