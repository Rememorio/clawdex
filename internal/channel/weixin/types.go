// Package weixin implements the Weixin (personal WeChat) channel driver.
//
// It uses long-polling via the iLink bot HTTP API (getUpdates / sendMessage)
// to bridge inbound Weixin messages to the gateway and deliver AI responses
// back to the user.
package weixin

// ── Protocol types (mirrors proto definitions) ──

// messageType constants for WeixinMessage.MessageType.
const (
	messageTypeUser = 1
	messageTypeBot  = 2
)

// messageItemType constants for MessageItem.Type.
const (
	itemTypeText  = 1
	itemTypeImage = 2
	itemTypeVoice = 3
	itemTypeFile  = 4
	itemTypeVideo = 5
)

// messageState constants for WeixinMessage.MessageState.
const (
	messageStateNew        = 0
	messageStateGenerating = 1
	messageStateFinish     = 2
)

// baseInfo is common metadata attached to every outgoing CGI request.
type baseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// cdnMedia is a CDN media reference with encryption parameters.
type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// textItem is a text content item.
type textItem struct {
	Text string `json:"text,omitempty"`
}

// imageItem is an image content item with CDN references.
type imageItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	ThumbMedia *cdnMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"` // hex-encoded 16 bytes
	URL        string    `json:"url,omitempty"`
}

// voiceItem is a voice content item with CDN reference and metadata.
type voiceItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	Playtime int       `json:"playtime,omitempty"` // milliseconds
	Text     string    `json:"text,omitempty"`     // transcription
}

// fileItem is a file content item.
type fileItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// videoItem is a video content item.
type videoItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	VideoSize  int       `json:"video_size,omitempty"`
	PlayLength int       `json:"play_length,omitempty"`
	VideoMD5   string    `json:"video_md5,omitempty"`
}

// messageItem is a single content item within a WeixinMessage.
type messageItem struct {
	Type         int        `json:"type,omitempty"`
	CreateTimeMs int64      `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64      `json:"update_time_ms,omitempty"`
	IsCompleted  bool       `json:"is_completed,omitempty"`
	MsgID        string     `json:"msg_id,omitempty"`
	TextItem     *textItem  `json:"text_item,omitempty"`
	ImageItem    *imageItem `json:"image_item,omitempty"`
	VoiceItem    *voiceItem `json:"voice_item,omitempty"`
	FileItem     *fileItem  `json:"file_item,omitempty"`
	VideoItem    *videoItem `json:"video_item,omitempty"`
}

// weixinMessage is the unified message type from the getUpdates response.
type weixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	UpdateTimeMs int64         `json:"update_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []messageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

// ── API request/response types ──

// getUpdatesReq is the request body for getupdates.
type getUpdatesReq struct {
	BaseInfo      *baseInfo `json:"base_info,omitempty"`
	GetUpdatesBuf string    `json:"get_updates_buf,omitempty"`
}

// getUpdatesResp is the response body from getupdates.
type getUpdatesResp struct {
	Ret                  int             `json:"ret"`
	ErrCode              int             `json:"errcode,omitempty"`
	ErrMsg               string          `json:"errmsg,omitempty"`
	Msgs                 []weixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMs int64           `json:"longpolling_timeout_ms,omitempty"`
}

// sendMessageReq is the request body for sendmessage.
type sendMessageReq struct {
	BaseInfo *baseInfo      `json:"base_info,omitempty"`
	Msg      *weixinMessage `json:"msg,omitempty"`
}

// sendMessageResp is the response body from sendmessage.
type sendMessageResp struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// sendTypingReq is the request body for sendtyping.
type sendTypingReq struct {
	BaseInfo     *baseInfo `json:"base_info,omitempty"`
	IlinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"` // 1=typing, 2=cancel
}

// sendTypingResp is the response body from sendtyping.
type sendTypingResp struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// getConfigResp is the response body from getconfig.
type getConfigResp struct {
	Ret          int    `json:"ret"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// notifyReq is the request body for notifystart / notifystop.
type notifyReq struct {
	BaseInfo *baseInfo `json:"base_info,omitempty"`
}

// notifyResp is the response body from notifystart / notifystop.
type notifyResp struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// getUploadURLReq is the request body for getuploadurl.
type getUploadURLReq struct {
	BaseInfo        *baseInfo `json:"base_info,omitempty"`
	FileKey         string    `json:"filekey,omitempty"`
	MediaType       int       `json:"media_type,omitempty"` // 1=image, 2=video, 3=file, 4=voice
	ToUserID        string    `json:"to_user_id,omitempty"`
	RawSize         int64     `json:"rawsize,omitempty"`
	RawFileMD5      string    `json:"rawfilemd5,omitempty"`
	FileSize        int64     `json:"filesize,omitempty"`
	ThumbRawSize    int64     `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string    `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int64     `json:"thumb_filesize,omitempty"`
	NoNeedThumb     bool      `json:"no_need_thumb,omitempty"`
	AESKeyStr       string    `json:"aeskey,omitempty"`
}

// getUploadURLResp is the response body from getuploadurl.
type getUploadURLResp struct {
	Ret              int    `json:"ret"`
	ErrMsg           string `json:"errmsg,omitempty"`
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}
