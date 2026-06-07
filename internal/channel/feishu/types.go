package feishu

import "context"

type textContent struct {
	Text string `json:"text"`
}

type messageAPI interface {
	ReplyText(ctx context.Context, messageID, text string, replyInThread bool) error
	SendText(ctx context.Context, chatID, text string) error
	BotOpenID(ctx context.Context) (string, error)
}
