package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/logger"
)

const (
	replyMediaChunkSize = 512 * 1024
	maxReplyMediaChunks = 100
	maxVoiceBytes       = 2 * 1024 * 1024
	maxVideoBytes       = 10 * 1024 * 1024
	voiceFileExt        = ".amr"
	videoFileExt        = ".mp4"
	defaultReplyName    = "attachment.bin"
)

type wsMediaRef struct {
	MediaID string `json:"media_id,omitempty"`
}

type wsVideoRef struct {
	MediaID     string `json:"media_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type wsUploadMediaInitBody struct {
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	TotalSize   int    `json:"total_size"`
	TotalChunks int    `json:"total_chunks"`
	MD5         string `json:"md5,omitempty"`
}

type wsUploadMediaInitAck struct {
	UploadID string `json:"upload_id,omitempty"`
}

type wsUploadMediaChunkBody struct {
	UploadID   string `json:"upload_id"`
	ChunkIndex int    `json:"chunk_index"`
	Base64Data string `json:"base64_data"`
}

type wsUploadMediaFinishBody struct {
	UploadID string `json:"upload_id"`
}

type wsUploadMediaFinishAck struct {
	Type      string `json:"type,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

type wsReplyMedia struct {
	MsgType     string
	Filename    string
	Data        []byte
	Title       string
	Description string
}

type wsMediaSession interface {
	send(ctx context.Context, frame wsOutboundFrame) error
	request(ctx context.Context, frame wsOutboundFrame) (wsInboundFrame, error)
}

func (d *Driver) replyWithWebSocketMedia(
	ctx context.Context,
	msg channel.Message,
	caption string,
	filePaths []string,
) error {
	if caption != "" {
		if err := d.replyViaWebSocket(ctx, msg, caption); err != nil {
			return err
		}
	}
	if len(filePaths) == 0 {
		return nil
	}

	d.mu.RLock()
	session := d.wsSession
	d.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("wecom websocket: no active session")
	}

	reqIDVal, ok := d.callbackReqIDs.Load(msg.ChatID)
	if !ok {
		return fmt.Errorf(
			"wecom websocket: no callback req_id for chat %d",
			msg.ChatID,
		)
	}
	baseReqID := reqIDVal.(string)

	for _, filePath := range filePaths {
		if err := sendWebSocketReplyMedia(
			ctx,
			session,
			baseReqID,
			filePath,
		); err != nil {
			logger.Warn(
				"wecom websocket send media failed",
				"channel", d.name,
				"file", filePath,
				"error", err,
			)
		}
	}
	return nil
}

func sendWebSocketReplyMedia(
	ctx context.Context,
	session wsMediaSession,
	baseReqID string,
	filePath string,
) error {
	media, err := loadWebSocketReplyMedia(filePath)
	if err != nil {
		return err
	}

	mediaID, err := uploadWebSocketReplyMedia(ctx, session, media)
	if err != nil {
		return err
	}
	return sendUploadedWebSocketReplyMedia(
		ctx,
		session,
		baseReqID,
		media,
		mediaID,
	)
}

func loadWebSocketReplyMedia(
	filePath string,
) (wsReplyMedia, error) {
	cleaned := filepath.Clean(strings.TrimSpace(filePath))
	if cleaned == "" {
		return wsReplyMedia{}, fmt.Errorf(
			"wecom websocket: empty reply file path",
		)
	}

	data, err := os.ReadFile(cleaned)
	if err != nil {
		return wsReplyMedia{}, fmt.Errorf(
			"wecom websocket: read reply file: %w",
			err,
		)
	}

	media := classifyWebSocketReplyMedia(cleaned, data)
	if err := validateWebSocketReplyMedia(media); err != nil {
		return wsReplyMedia{}, err
	}
	return media, nil
}

func classifyWebSocketReplyMedia(filePath string, data []byte) wsReplyMedia {
	filename := filepath.Base(filePath)
	if filename == "." || filename == string(filepath.Separator) {
		filename = defaultReplyName
	}

	ext := strings.ToLower(filepath.Ext(filename))
	media := wsReplyMedia{
		MsgType:  "file",
		Filename: filename,
		Data:     data,
	}
	switch {
	case isWebSocketReplyImageExt(ext) && len(data) <= maxImageBytes:
		media.MsgType = "image"
	case ext == voiceFileExt && len(data) <= maxVoiceBytes:
		media.MsgType = "voice"
	case ext == videoFileExt && len(data) <= maxVideoBytes:
		media.MsgType = "video"
		media.Title = strings.TrimSuffix(filename, ext)
	default:
		media.MsgType = "file"
	}
	if strings.TrimSpace(media.Title) == "" {
		media.Title = strings.TrimSuffix(filename, ext)
	}
	return media
}

func isWebSocketReplyImageExt(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	default:
		return false
	}
}

func validateWebSocketReplyMedia(media wsReplyMedia) error {
	size := len(media.Data)
	if size <= 0 {
		return fmt.Errorf("wecom websocket: reply file is empty")
	}

	limit := maxMediaBytes
	switch media.MsgType {
	case "image":
		limit = maxImageBytes
	case "voice":
		limit = maxVoiceBytes
	case "video":
		limit = maxVideoBytes
	}
	if size > limit {
		return fmt.Errorf(
			"wecom websocket: reply file %q exceeds %d bytes for %s",
			media.Filename,
			limit,
			media.MsgType,
		)
	}

	chunks := replyMediaChunks(size)
	if chunks > maxReplyMediaChunks {
		return fmt.Errorf(
			"wecom websocket: reply file %q needs %d chunks",
			media.Filename,
			chunks,
		)
	}
	return nil
}

func replyMediaChunks(size int) int {
	if size <= 0 {
		return 0
	}
	chunks := size / replyMediaChunkSize
	if size%replyMediaChunkSize != 0 {
		chunks++
	}
	return chunks
}

func uploadWebSocketReplyMedia(
	ctx context.Context,
	session wsMediaSession,
	media wsReplyMedia,
) (string, error) {
	uploadID, err := initWebSocketReplyUpload(ctx, session, media)
	if err != nil {
		return "", err
	}
	if err := uploadWebSocketReplyChunks(ctx, session, uploadID, media.Data); err != nil {
		return "", err
	}
	return finishWebSocketReplyUpload(ctx, session, uploadID)
}

func initWebSocketReplyUpload(
	ctx context.Context,
	session wsMediaSession,
	media wsReplyMedia,
) (string, error) {
	sum := md5.Sum(media.Data)
	ack, err := session.request(ctx, wsOutboundFrame{
		Command: wsCommandUploadMediaInit,
		Headers: wsFrameHeaders{ReqID: nextWSReqID("upload-init")},
		Body: wsUploadMediaInitBody{
			Type:        media.MsgType,
			Filename:    media.Filename,
			TotalSize:   len(media.Data),
			TotalChunks: replyMediaChunks(len(media.Data)),
			MD5:         hex.EncodeToString(sum[:]),
		},
	})
	if err != nil {
		return "", err
	}

	var body wsUploadMediaInitAck
	if err := unmarshalWebSocketFrameBody(ack, &body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.UploadID) == "" {
		return "", fmt.Errorf("wecom websocket: empty upload_id")
	}
	return body.UploadID, nil
}

func uploadWebSocketReplyChunks(
	ctx context.Context,
	session wsMediaSession,
	uploadID string,
	data []byte,
) error {
	for index, start := 0, 0; start < len(data); index++ {
		end := start + replyMediaChunkSize
		if end > len(data) {
			end = len(data)
		}

		_, err := session.request(ctx, wsOutboundFrame{
			Command: wsCommandUploadMediaChunk,
			Headers: wsFrameHeaders{ReqID: nextWSReqID("upload-chunk")},
			Body: wsUploadMediaChunkBody{
				UploadID:   uploadID,
				ChunkIndex: index,
				Base64Data: base64.StdEncoding.EncodeToString(data[start:end]),
			},
		})
		if err != nil {
			return err
		}
		start = end
	}
	return nil
}

func finishWebSocketReplyUpload(
	ctx context.Context,
	session wsMediaSession,
	uploadID string,
) (string, error) {
	ack, err := session.request(ctx, wsOutboundFrame{
		Command: wsCommandUploadMediaFinish,
		Headers: wsFrameHeaders{ReqID: nextWSReqID("upload-finish")},
		Body:    wsUploadMediaFinishBody{UploadID: uploadID},
	})
	if err != nil {
		return "", err
	}

	var body wsUploadMediaFinishAck
	if err := unmarshalWebSocketFrameBody(ack, &body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.MediaID) == "" {
		return "", fmt.Errorf("wecom websocket: empty media_id")
	}
	return body.MediaID, nil
}

func sendUploadedWebSocketReplyMedia(
	ctx context.Context,
	session wsMediaSession,
	baseReqID string,
	media wsReplyMedia,
	mediaID string,
) error {
	body := wsReplyBody{MsgType: media.MsgType}
	switch media.MsgType {
	case "image":
		body.Image = &wsMediaRef{MediaID: mediaID}
	case "voice":
		body.Voice = &wsMediaRef{MediaID: mediaID}
	case "video":
		body.Video = &wsVideoRef{MediaID: mediaID, Title: media.Title}
	default:
		body.MsgType = "file"
		body.File = &wsMediaRef{MediaID: mediaID}
	}

	return session.send(ctx, wsOutboundFrame{
		Command: wsCommandRespond,
		Headers: wsFrameHeaders{ReqID: baseReqID},
		Body:    body,
	})
}

func unmarshalWebSocketFrameBody(frame wsInboundFrame, target any) error {
	if len(frame.Body) == 0 {
		return fmt.Errorf("wecom websocket: empty ack body")
	}
	if err := json.Unmarshal(frame.Body, target); err != nil {
		return fmt.Errorf("wecom websocket: unmarshal ack body: %w", err)
	}
	return nil
}
