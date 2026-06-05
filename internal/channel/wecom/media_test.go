package wecom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedWSFrame struct {
	Command string
	Headers wsFrameHeaders
	Body    json.RawMessage
}

func TestReplyWithMedia_WebSocketFileUpload(t *testing.T) {
	frames, wsURL, shutdown := startWeComWebSocketTestServer(t)
	defer shutdown()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	d := New(Config{ConnectionMode: "websocket"}, nil)
	session := newWSSession(conn)
	d.wsSession = session
	d.callbackReqIDs.Store(int64(1), "callback-req")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = d.readWSFrames(ctx, session)
	}()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "report.pdf")
	fileData := []byte("pdf reply content")
	require.NoError(t, os.WriteFile(filePath, fileData, 0o600))

	err = d.ReplyWithMedia(
		context.Background(),
		channel.Message{ChatID: 1},
		"done",
		[]string{filePath},
	)
	require.NoError(t, err)

	captionFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, captionFrame.Command)
	var captionBody wsReplyBody
	require.NoError(t, json.Unmarshal(captionFrame.Body, &captionBody))
	assert.Equal(t, "markdown", captionBody.MsgType)
	require.NotNil(t, captionBody.Markdown)
	assert.Equal(t, "done", captionBody.Markdown.Content)

	initFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandUploadMediaInit, initFrame.Command)
	var initBody wsUploadMediaInitBody
	require.NoError(t, json.Unmarshal(initFrame.Body, &initBody))
	assert.Equal(t, "file", initBody.Type)
	assert.Equal(t, "report.pdf", initBody.Filename)
	assert.Equal(t, len(fileData), initBody.TotalSize)
	assert.Equal(t, 1, initBody.TotalChunks)
	assert.NotEmpty(t, initBody.MD5)

	chunkFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandUploadMediaChunk, chunkFrame.Command)
	var chunkBody wsUploadMediaChunkBody
	require.NoError(t, json.Unmarshal(chunkFrame.Body, &chunkBody))
	assert.Equal(t, "upload-1", chunkBody.UploadID)
	assert.Equal(t, 0, chunkBody.ChunkIndex)
	decodedChunk, err := base64.StdEncoding.DecodeString(chunkBody.Base64Data)
	require.NoError(t, err)
	assert.Equal(t, fileData, decodedChunk)

	finishFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandUploadMediaFinish, finishFrame.Command)
	var finishBody wsUploadMediaFinishBody
	require.NoError(t, json.Unmarshal(finishFrame.Body, &finishBody))
	assert.Equal(t, "upload-1", finishBody.UploadID)

	mediaFrame := mustReceiveWSFrame(t, frames)
	assert.Equal(t, wsCommandRespond, mediaFrame.Command)
	var mediaBody wsReplyBody
	require.NoError(t, json.Unmarshal(mediaFrame.Body, &mediaBody))
	assert.Equal(t, "file", mediaBody.MsgType)
	require.NotNil(t, mediaBody.File)
	assert.Equal(t, "media-1", mediaBody.File.MediaID)
}

func TestClassifyWebSocketReplyMedia(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		data     []byte
		msgType  string
	}{
		{
			name:     "image",
			filePath: "/tmp/demo.png",
			data:     []byte("png"),
			msgType:  "image",
		},
		{
			name:     "voice",
			filePath: "/tmp/demo.amr",
			data:     []byte("amr"),
			msgType:  "voice",
		},
		{
			name:     "video",
			filePath: "/tmp/demo.mp4",
			data:     []byte("mp4"),
			msgType:  "video",
		},
		{
			name:     "large image falls back to file",
			filePath: "/tmp/demo.png",
			data:     make([]byte, maxImageBytes+1),
			msgType:  "file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media := classifyWebSocketReplyMedia(tt.filePath, tt.data)
			assert.Equal(t, tt.msgType, media.MsgType)
		})
	}
}

func TestExtractWSContent_Video(t *testing.T) {
	d := New(Config{Token: testToken, EncodingAESKey: testEncodingAESKey}, nil)
	msg := &wsMessage{
		MsgType: "video",
		Video: wsFileContent{
			URL:    "https://example.com/demo.mp4",
			AESKey: "video-key",
		},
	}
	text, urls, keys := d.extractWSContent(msg)
	assert.Equal(t, "[video]", text)
	assert.Equal(t, []string{"https://example.com/demo.mp4"}, urls)
	assert.Equal(t, []string{"video-key"}, keys)
}

func startWeComWebSocketTestServer(
	t *testing.T,
) (<-chan capturedWSFrame, string, func()) {
	t.Helper()

	frames := make(chan capturedWSFrame, 16)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var raw struct {
				Command string          `json:"cmd"`
				Headers wsFrameHeaders  `json:"headers,omitempty"`
				Body    json.RawMessage `json:"body,omitempty"`
			}
			require.NoError(t, json.Unmarshal(data, &raw))
			frames <- capturedWSFrame{
				Command: raw.Command,
				Headers: raw.Headers,
				Body:    raw.Body,
			}

			switch raw.Command {
			case wsCommandUploadMediaInit:
				require.NoError(t, writeWSTestAck(
					conn,
					raw.Command,
					raw.Headers.ReqID,
					wsUploadMediaInitAck{UploadID: "upload-1"},
				))
			case wsCommandUploadMediaChunk:
				require.NoError(t, writeWSTestAck(
					conn,
					raw.Command,
					raw.Headers.ReqID,
					map[string]any{"ok": true},
				))
			case wsCommandUploadMediaFinish:
				require.NoError(t, writeWSTestAck(
					conn,
					raw.Command,
					raw.Headers.ReqID,
					wsUploadMediaFinishAck{MediaID: "media-1"},
				))
			}
		}
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	shutdown := func() {
		server.Close()
	}
	return frames, wsURL, shutdown
}

func writeWSTestAck(
	conn *websocket.Conn,
	cmd string,
	reqID string,
	body any,
) error {
	payload := map[string]any{
		"cmd": cmd,
		"headers": map[string]any{
			"req_id": reqID,
		},
		"body":    body,
		"errcode": 0,
		"errmsg":  "ok",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func mustReceiveWSFrame(
	t *testing.T,
	frames <-chan capturedWSFrame,
) capturedWSFrame {
	t.Helper()

	select {
	case frame := <-frames:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket frame")
		return capturedWSFrame{}
	}
}
